package secret

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/ioutil"
	"regexp"

	"git.vzbuilders.com/marshadrad/panoptes/config"
	"git.vzbuilders.com/marshadrad/panoptes/secret/vault"
)

type Secret interface {
	GetSecrets(string) (map[string][]byte, error)
}

func GetSecretEngine(sType string) (Secret, error) {
	switch sType {
	case "vault":
		return vault.New()
	}

	return nil, fmt.Errorf("%s secret engine doesn't support", sType)
}

func GetTLSConfig(cfg *config.TLSConfig) (*tls.Config, error) {
	tlsConfig, ok, err := getTLSConfigRemote(cfg)
	if ok {
		return tlsConfig, err
	}

	return getTLSConfigLocal(cfg)
}

func GetCredentials(key string) (map[string]string, bool, error) {
	sType, path, ok := ParseRemoteSecretInfo(key)
	if ok {
		sec, err := GetSecretEngine(sType)
		if err != nil {
			return nil, ok, err
		}

		secrets, err := sec.GetSecrets(path)
		if err != nil {
			return nil, ok, err
		}

		result := make(map[string]string)
		for k, v := range secrets {
			result[k] = string(v)
		}

		return result, ok, nil
	}

	return nil, ok, errors.New("uknown remote secret information")
}

func ParseRemoteSecretInfo(key string) (string, string, bool) {
	re := regexp.MustCompile(`__([a-zA-Z0-9]*)::(.*)`)
	match := re.FindStringSubmatch(key)
	if len(match) < 1 {
		return "", "", false
	}

	return match[1], match[2], true
}

func getTLSConfigRemote(cfg *config.TLSConfig) (*tls.Config, bool, error) {
	var caCertPool *x509.CertPool

	sType, path, ok := ParseRemoteSecretInfo(cfg.CertFile)
	if ok {
		sec, err := GetSecretEngine(sType)
		if err != nil {
			return nil, ok, err
		}

		secrets, err := sec.GetSecrets(path)
		if err != nil {
			return nil, ok, err
		}

		if !isExist(secrets, "cert") || !isExist(secrets, "key") {
			return nil, ok, errors.New("cert and private key is not available")
		}

		cert, err := tls.X509KeyPair(secrets["cert"], secrets["key"])
		if err != nil {
			return nil, ok, err
		}

		if isExist(secrets, "ca") {
			caCertPool = x509.NewCertPool()
			caCertPool.AppendCertsFromPEM(secrets["ca"])
		}

		return &tls.Config{
			Certificates:       []tls.Certificate{cert},
			RootCAs:            caCertPool,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		}, ok, nil
	}

	return nil, false, nil
}

func getTLSConfigLocal(cfg *config.TLSConfig) (*tls.Config, error) {
	var caCertPool *x509.CertPool

	// combined cert and private key
	if len(cfg.KeyFile) < 1 {
		cfg.KeyFile = cfg.CertFile
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}

	if cfg.CAFile != "" {
		caCert, err := ioutil.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}

		caCertPool = x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(caCert)
	}

	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		RootCAs:            caCertPool,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}, nil
}

func isExist(m map[string][]byte, k string) bool {
	_, ok := m[k]
	return ok
}