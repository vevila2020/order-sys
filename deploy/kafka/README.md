# Kafka topic 初始化

M0 不创建业务 topic；Kafka 仅在 Compose 中提供 broker，供 M3 接入。

M3 启用时在此目录添加创建脚本，并由 Compose 的一次性容器执行：

```bash
kafka-topics.sh --bootstrap-server kafka:9092 \
  --create --if-not-exists \
  --topic order.events --partitions 3 --replication-factor 1
```

约定：

- 分区键使用 `aggregate_id`，保证同一聚合内顺序可解释。
- 副本数 1 仅用于本地；分区数与容量模型一致（见需求 §12.7）。
- 不使用 `auto.create.topics.enable`，避免拼写错误静默创建 topic。
