package harness

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/segmentio/kafka-go"
)

// Kafka produces into the topics the service consumes, and reads the group's
// position back.
//
// Two mechanics here have no MQTT equivalent and carry most of a Kafka suite's
// value: WaitForGroupCaughtUp is the broker's own account of what the service
// has consumed — the right wait for scenarios asserting that nothing was
// stored — and ResetGroup provokes the replay that proves the service is
// idempotent under the at-least-once delivery Kafka actually promises.
type Kafka struct {
	Brokers []string
	writer  *kafka.Writer
}

// ConnectKafka prepares a producer. Balancer is by key, so messages with the
// same key land on the same partition and per-partition ordering scenarios
// mean what they say.
func ConnectKafka(brokers []string) *Kafka {
	return &Kafka{
		Brokers: brokers,
		writer: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Balancer:               &kafka.Hash{},
			AllowAutoTopicCreation: false,
			RequiredAcks:           kafka.RequireAll,
		},
	}
}

// Close flushes and releases the producer.
func (k *Kafka) Close() error { return k.writer.Close() }

// CreateTopic creates a topic with an explicit partition count and waits for
// the metadata to propagate. Relying on auto-creation races the first produce
// and gives the topic one partition, which quietly destroys ordering
// scenarios.
func (k *Kafka) CreateTopic(ctx context.Context, topic string, partitions int) error {
	conn, err := kafka.DialContext(ctx, "tcp", k.Brokers[0])
	if err != nil {
		return fmt.Errorf("dialling kafka at %s: %w", k.Brokers[0], err)
	}
	defer conn.Close()

	controller, err := conn.Controller()
	if err != nil {
		return fmt.Errorf("finding kafka controller: %w", err)
	}
	admin, err := kafka.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", controller.Host, controller.Port))
	if err != nil {
		return fmt.Errorf("dialling kafka controller: %w", err)
	}
	defer admin.Close()

	err = admin.CreateTopics(kafka.TopicConfig{
		Topic: topic, NumPartitions: partitions, ReplicationFactor: 1,
	})
	if err != nil && !errors.Is(err, kafka.TopicAlreadyExists) {
		return fmt.Errorf("creating topic %s: %w", topic, err)
	}
	return Until(ctx, 20*time.Second, "topic "+topic+" metadata", func(ctx context.Context) (bool, error) {
		parts, err := conn.ReadPartitions(topic)
		return len(parts) >= partitions, err
	})
}

// Publish sends one message. Messages sharing a key share a partition, which
// is the only ordering Kafka promises.
func (k *Kafka) Publish(ctx context.Context, topic, key string, body any, headers ...kafka.Header) error {
	payload, err := encodePayload(body)
	if err != nil {
		return err
	}
	msg := kafka.Message{Topic: topic, Key: []byte(key), Value: payload, Headers: headers}
	if err := k.writer.WriteMessages(ctx, msg); err != nil {
		return fmt.Errorf("publishing to %s: %w", topic, err)
	}
	return nil
}

// PublishMarked adds the receipt marker as a header rather than a payload
// field, so it works even where the payload schema forbids unknown fields.
func (k *Kafka) PublishMarked(ctx context.Context, topic, key string, body any, scenario string) error {
	return k.Publish(ctx, topic, key, body, kafka.Header{Key: MarkerField, Value: []byte(scenario)})
}

// PublishToPartition targets one partition explicitly, for the scenario where
// a poison message must not stall everything behind it on the same partition.
func (k *Kafka) PublishToPartition(ctx context.Context, topic string, partition int, body any) error {
	payload, err := encodePayload(body)
	if err != nil {
		return err
	}
	writer := &kafka.Writer{Addr: kafka.TCP(k.Brokers...), Balancer: &kafka.LeastBytes{}}
	defer writer.Close()
	return writer.WriteMessages(ctx, kafka.Message{Topic: topic, Partition: partition, Value: payload})
}

// Lag is how far the consumer group is behind across every partition of a
// topic. Zero means everything published so far has been consumed.
func (k *Kafka) Lag(ctx context.Context, group, topic string) (int64, error) {
	conn, err := kafka.DialContext(ctx, "tcp", k.Brokers[0])
	if err != nil {
		return 0, fmt.Errorf("dialling kafka: %w", err)
	}
	defer conn.Close()

	partitions, err := conn.ReadPartitions(topic)
	if err != nil {
		return 0, fmt.Errorf("reading partitions of %s: %w", topic, err)
	}
	client := &kafka.Client{Addr: kafka.TCP(k.Brokers...), Timeout: 10 * time.Second}

	offsetReq := &kafka.OffsetFetchRequest{GroupID: group, Topics: map[string][]int{}}
	ends := map[int]int64{}
	for _, p := range partitions {
		offsetReq.Topics[topic] = append(offsetReq.Topics[topic], p.ID)

		leader, err := kafka.DialLeader(ctx, "tcp", k.Brokers[0], topic, p.ID)
		if err != nil {
			return 0, fmt.Errorf("dialling leader of %s/%d: %w", topic, p.ID, err)
		}
		last, err := leader.ReadLastOffset()
		leader.Close()
		if err != nil {
			return 0, fmt.Errorf("reading last offset of %s/%d: %w", topic, p.ID, err)
		}
		ends[p.ID] = last
	}

	fetched, err := client.OffsetFetch(ctx, offsetReq)
	if err != nil {
		return 0, fmt.Errorf("fetching offsets for group %s: %w", group, err)
	}
	var lag int64
	for _, p := range fetched.Topics[topic] {
		committed := p.CommittedOffset
		if committed < 0 {
			committed = 0 // the group has committed nothing yet
		}
		if behind := ends[p.Partition] - committed; behind > 0 {
			lag += behind
		}
	}
	return lag, nil
}

// WaitForGroupCaughtUp waits until the group's lag reaches zero: the broker's
// own statement that the service has consumed everything published so far.
// This is the right wait before asserting that nothing was stored.
func (k *Kafka) WaitForGroupCaughtUp(ctx context.Context, group, topic string, timeout time.Duration) error {
	var last int64
	err := Until(ctx, timeout, fmt.Sprintf("consumer group %s to catch up on %s", group, topic),
		func(ctx context.Context) (bool, error) {
			lag, err := k.Lag(ctx, group, topic)
			if err != nil {
				return false, err
			}
			last = lag
			return lag == 0, nil
		})
	if err != nil {
		return fmt.Errorf("%w (lag was %d)", err, last)
	}
	return nil
}

// ResetGroup moves the group back to the earliest offset, so restarting the
// service replays everything. This is the Kafka form of the retained-message
// replay: the assertion afterwards is that no record was written twice.
// The service must not be running, or the broker rejects the reset.
func (k *Kafka) ResetGroup(ctx context.Context, group, topic string) error {
	conn, err := kafka.DialContext(ctx, "tcp", k.Brokers[0])
	if err != nil {
		return fmt.Errorf("dialling kafka: %w", err)
	}
	defer conn.Close()

	partitions, err := conn.ReadPartitions(topic)
	if err != nil {
		return fmt.Errorf("reading partitions of %s: %w", topic, err)
	}
	offsets := map[string]map[int]int64{topic: {}}
	for _, p := range partitions {
		leader, err := kafka.DialLeader(ctx, "tcp", k.Brokers[0], topic, p.ID)
		if err != nil {
			return fmt.Errorf("dialling leader of %s/%d: %w", topic, p.ID, err)
		}
		first, err := leader.ReadFirstOffset()
		leader.Close()
		if err != nil {
			return fmt.Errorf("reading first offset of %s/%d: %w", topic, p.ID, err)
		}
		offsets[topic][p.ID] = first
	}

	group2, err := kafka.NewConsumerGroup(kafka.ConsumerGroupConfig{
		ID: group, Brokers: k.Brokers, Topics: []string{topic},
	})
	if err != nil {
		return fmt.Errorf("joining group %s to reset it: %w", group, err)
	}
	defer group2.Close()

	generation, err := group2.Next(ctx)
	if err != nil {
		return fmt.Errorf("taking a generation of group %s: %w", group, err)
	}
	if err := generation.CommitOffsets(offsets); err != nil {
		return fmt.Errorf("resetting offsets of group %s: %w", group, err)
	}
	return nil
}

// Read drains up to max messages from a topic, for asserting on what the
// service produced (an outbound topic, or a dead-letter topic).
func (k *Kafka) Read(ctx context.Context, topic string, max int, timeout time.Duration) ([]kafka.Message, error) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers: k.Brokers, Topic: topic, StartOffset: kafka.FirstOffset,
	})
	defer reader.Close()

	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var out []kafka.Message
	for len(out) < max {
		msg, err := reader.ReadMessage(deadline)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return out, nil // the window closed: return what arrived
			}
			return out, fmt.Errorf("reading from %s: %w", topic, err)
		}
		out = append(out, msg)
	}
	return out, nil
}
