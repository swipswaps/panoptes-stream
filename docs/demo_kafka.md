### Single node with Kafka demonstration 
---

![panoptes demo](imgs/demo_kafka.png)

##### Checkout GitHub
```
git checkout github.com/yahoo/panoptes-stream
cd panoptes-stream/scripts/demo
```


##### Start the containers
```console
docker-compose -f docker-compose.kafka.yml up -d
```

```console
docker-compose -f docker-compose.kafka.yml ps
```
![panoptes kafka](/docs/imgs/demo_kafka_dc_ps.png)

```consol
docker exec -it kafka bash
cd /opt/bitnami/kafka/bin
kafka-console-consumer.sh --bootstrap-server localhost:9092 --topic ifcounters --from-beginning
kafka-topics.sh --describe --zookeeper zookeeper:2181 --topic ifcounters
```

##### Clean up
```console
docker-compose -f docker-compose.kafka.yml down
```

 <span style="color:purple">All demonstrations</span>
Please check out the [demo page](demo_list.md) to see all of the demonstrations for different scenarios.  