package gnmi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/golang/protobuf/ptypes"
	"github.com/openconfig/gnmi/path"
	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/ygot/ygot"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/anypb"

	"git.vzbuilders.com/marshadrad/panoptes/config"
	"git.vzbuilders.com/marshadrad/panoptes/status"
	"git.vzbuilders.com/marshadrad/panoptes/telemetry"
	"git.vzbuilders.com/marshadrad/panoptes/telemetry/juniper/proto/GnmiJuniperTelemetryHeader"
)

var (
	gnmiVersion = "0.7.0"

	labelsRegex = regexp.MustCompile("(\\/[^\\/]*)\\[([A-Za-z0-9\\-\\/]*\\=[^\\[]*)\\]")

	metricGRPCDataTotal = status.NewCounter("juniper_gnmi_grpc_data_total", "")
	metricDropsTotal    = status.NewCounter("juniper_gnmi_drops_total", "")
	metricErrorsTotal   = status.NewCounter("juniper_gnmi_errors_total", "")
)

// GNMI represents a GNMI Juniper.
type GNMI struct {
	conn          *grpc.ClientConn
	subscriptions []*gpb.Subscription

	dataChan chan *gpb.SubscribeResponse
	outChan  telemetry.ExtDSChan
	logger   *zap.Logger

	pathOutput map[string]string
}

// New creates a GNMI.
func New(logger *zap.Logger, conn *grpc.ClientConn, sensors []*config.Sensor, outChan telemetry.ExtDSChan) telemetry.NMI {
	var (
		subscriptions = []*gpb.Subscription{}
		pathOutput    = make(map[string]string)
	)

	status.Register(
		status.Labels{"host": conn.Target()},
		metricGRPCDataTotal,
		metricDropsTotal,
		metricErrorsTotal,
	)

	for _, sensor := range sensors {
		path, _ := ygot.StringToPath(sensor.Path, ygot.StructuredPath, ygot.StringSlicePath)

		mode := gpb.SubscriptionMode_value[strings.ToUpper(sensor.Mode)]
		sampleInterval := time.Duration(sensor.SampleInterval) * time.Second
		heartbeatInterval := time.Duration(sensor.HeartbeatInterval) * time.Second
		subscriptions = append(subscriptions, &gpb.Subscription{
			Path:              path,
			Mode:              gpb.SubscriptionMode(mode),
			SampleInterval:    uint64(sampleInterval.Nanoseconds()),
			HeartbeatInterval: uint64(heartbeatInterval.Nanoseconds()),
			SuppressRedundant: sensor.SuppressRedundant,
		})

		if strings.HasSuffix(sensor.Path, "/") {
			pathOutput[sensor.Path] = sensor.Output
		} else {
			pathOutput[fmt.Sprintf("%s/", sensor.Path)] = sensor.Output
		}
	}

	return &GNMI{
		logger:        logger,
		conn:          conn,
		subscriptions: subscriptions,
		dataChan:      make(chan *gpb.SubscribeResponse, 100),
		outChan:       outChan,
		pathOutput:    pathOutput,
	}
}

// Start starts to get stream and fan-out to workers
func (g *GNMI) Start(ctx context.Context) error {
	client := gpb.NewGNMIClient(g.conn)
	subReq := &gpb.SubscribeRequest{
		Request: &gpb.SubscribeRequest_Subscribe{
			Subscribe: &gpb.SubscriptionList{
				Mode:         gpb.SubscriptionList_STREAM,
				Encoding:     gpb.Encoding(gpb.Encoding_value["PROTO"]),
				Subscription: g.subscriptions,
				UpdatesOnly:  false,
			},
		},
	}

	subClient, err := client.Subscribe(ctx)
	if err != nil {
		return err
	}

	err = subClient.Send(subReq)
	if err != nil {
		return err
	}

	for i := 0; i < 1; i++ {
		go g.worker(ctx)
	}

	for ctx.Err() == nil {
		resp, err := subClient.Recv()
		if err != nil && ctx.Err() == nil {
			return err
		}

		if resp != nil {
			g.dataChan <- resp
			metricGRPCDataTotal.Inc()
		}
	}

	return nil
}
func (g *GNMI) worker(ctx context.Context) {
	for {
		select {
		case d, ok := <-g.dataChan:
			if !ok {
				return
			}

			resp, ok := d.Response.(*gpb.SubscribeResponse_Update)
			if !ok {
				continue
			}

			ds := g.rawDataStore(resp)

			path, err := getPath(ds)
			if err != nil {
				metricErrorsTotal.Inc()
				g.logger.Warn("juniper.gnmi", zap.String("msg", "path not found"))
				continue
			}

			output, ok := g.pathOutput[path]
			if !ok {
				metricErrorsTotal.Inc()
				g.logger.Error("juniper.gnmi", zap.String("msg", "output lookup failed"), zap.String("path", path))
				continue
			}

			if isRawRequested(output) {
				select {
				case g.outChan <- telemetry.ExtDataStore{
					DS:     ds,
					Output: output,
				}:
				default:
					metricDropsTotal.Inc()
					g.logger.Error("juniper.gnmi", zap.String("msg", "dataset drop"))
				}
			} else {
				g.splitRawDataStore(ds, output)
			}

		case <-ctx.Done():
			return
		}
	}
}

func (g *GNMI) rawDataStore(resp *gpb.SubscribeResponse_Update) telemetry.DataStore {
	ds := make(telemetry.DataStore)
	ds["__service__"] = fmt.Sprintf("gnmi_v%s", gnmiVersion)

	ds["__update_timestamp__"] = resp.Update.GetTimestamp()
	ds["__prefix__"], _ = ygot.PathToString(resp.Update.GetPrefix())

	for _, update := range resp.Update.Update {

		pathSlice := path.ToStrings(update.Path, false)
		key := strings.Join(pathSlice, "/")

		value, err := getValue(update.Val)
		if err != nil {
			metricErrorsTotal.Inc()
			g.logger.Error("juniper.gnmi", zap.Error(err))
			continue
		}
		ds[key] = value

	}

	return ds
}

func (g *GNMI) splitRawDataStore(ds telemetry.DataStore, output string) {
	labels, prefix := getLabels(ds["__prefix__"].(string))
	systemID, _, _ := net.SplitHostPort(g.conn.Target())

	for key, value := range ds {
		if !strings.HasPrefix(key, "__") {
			dataStore := telemetry.DataStore{
				"__prefix":    prefix,
				"__labels":    labels,
				"__timestamp": ds["__update_timestamp__"],
				"__system_id": systemID,

				key: value,
			}

			select {
			case g.outChan <- telemetry.ExtDataStore{
				DS:     dataStore,
				Output: output,
			}:
			default:
				metricDropsTotal.Inc()
				g.logger.Warn("juniper.gnmi", zap.String("error", "dataset drop"))
			}
		}
	}

}

func getPath(ds telemetry.DataStore) (string, error) {
	regxPath := regexp.MustCompile("/:(/.*/):")

	if _, ok := ds["__juniper_telemetry_header__"]; ok {
		h := ds["__juniper_telemetry_header__"].(GnmiJuniperTelemetryHeader.GnmiJuniperTelemetryHeader)
		path := regxPath.FindStringSubmatch(h.Path)
		if len(path) > 1 {
			return path[1], nil
		}
	}

	return "", errors.New("path not found")
}

func getValue(tv *gpb.TypedValue) (interface{}, error) {
	var (
		jsondata []byte
		value    interface{}
		err      error
	)

	switch tv.Value.(type) {
	case *gpb.TypedValue_AsciiVal:
		value = tv.GetAsciiVal()
	case *gpb.TypedValue_BoolVal:
		value = tv.GetBoolVal()
	case *gpb.TypedValue_BytesVal:
		value = tv.GetBytesVal()
	case *gpb.TypedValue_DecimalVal:
		value = float64(tv.GetDecimalVal().Digits) / math.Pow(10, float64(tv.GetDecimalVal().Precision))
	case *gpb.TypedValue_FloatVal:
		value = tv.GetFloatVal()
	case *gpb.TypedValue_IntVal:
		value = tv.GetIntVal()
	case *gpb.TypedValue_StringVal:
		value = tv.GetStringVal()
	case *gpb.TypedValue_UintVal:
		value = tv.GetUintVal()
	case *gpb.TypedValue_JsonIetfVal:
		jsondata = tv.GetJsonIetfVal()
	case *gpb.TypedValue_JsonVal:
		jsondata = tv.GetJsonVal()
	case *gpb.TypedValue_AnyVal:
		value, err = getAnyVal(tv.GetAnyVal())
	case *gpb.TypedValue_LeaflistVal:
		elems := tv.GetLeaflistVal().GetElement()
		value, err = getLeafList(elems)
	default:
		err = fmt.Errorf("unknown value type %+v", tv.Value)

	}

	if jsondata != nil {
		err = json.Unmarshal(jsondata, &value)
	}

	return value, err
}

func getLeafList(elems []*gpb.TypedValue) (interface{}, error) {
	list := []interface{}{}
	for _, v := range elems {
		ev, err := getValue(v)
		if err != nil {
			return nil, fmt.Errorf("leaflist error: %v", err)
		}
		list = append(list, ev)
	}

	return list, nil
}

func getAnyVal(anyMsg *anypb.Any) (interface{}, error) {
	anyMsgName, err := ptypes.AnyMessageName(anyMsg)
	if err != nil {
		return nil, fmt.Errorf("proto any message: %v", err)
	}
	if anyMsgName == "GnmiJuniperTelemetryHeader" {
		hdr := GnmiJuniperTelemetryHeader.GnmiJuniperTelemetryHeader{}
		if err := ptypes.UnmarshalAny(anyMsg, &hdr); err != nil {
			return nil, err
		}
		return hdr, nil
	}

	return nil, fmt.Errorf("proto any unknown msg type")
}

func isRawRequested(output string) bool {
	return strings.HasSuffix(output, "::raw")
}

func getLabels(prefix string) (map[string]string, string) {
	labels := make(map[string]string)
	subs := labelsRegex.FindAllStringSubmatch(prefix, -1)
	for _, sub := range subs {
		if len(sub) != 3 {
			continue
		}
		kv := strings.Split(sub[2], "=")
		labels[kv[0]] = strings.ReplaceAll(kv[1], "'", "")
		prefix = strings.Replace(prefix, sub[0], sub[1], 1)
	}

	return labels, prefix
}

func Version() string {
	return gnmiVersion
}
