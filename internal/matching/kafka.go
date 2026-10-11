package matching

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

// Group is the single consumer group of this process.
const Group = "matchers"

// Publisher sends a result event. The Kafka key is ride_id.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// Kafka publishes matched and match-failed and consumes rides.requested.
type Kafka struct {
	cl    *kgo.Client
	seeds []string
	log   *slog.Logger
}

// NewKafka returns a bus. seeds is the broker list passed to depend.OpenKafka.
func NewKafka(cl *kgo.Client, brokers string, log *slog.Logger) *Kafka {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Kafka{cl: cl, seeds: splitSeeds(brokers), log: log}
}

func (k *Kafka) Publish(ctx context.Context, topic, key string, value []byte) error {
	res := k.cl.ProduceSync(ctx, &kgo.Record{
		Topic: topic,
		Key:   []byte(key),
		Value: value,
	})
	if err := res.FirstErr(); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

// Consume reads rides.requested. A record is committed only after handle returns nil.
// It returns when ctx is canceled.
func (k *Kafka) Consume(ctx context.Context, handle func(context.Context, string, []byte) error) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(k.seeds...),
		kgo.ConsumerGroup(Group),
		kgo.ConsumeTopics(event.TopicRidesRequested),
		kgo.DisableAutoCommit(),
		kgo.DialTimeout(5*time.Second),
	)
	if err != nil {
		return fmt.Errorf("matching consumer: %w", err)
	}
	defer cl.Close()

	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if fetches.IsClientClosed() {
			return fmt.Errorf("matching consumer closed")
		}
		k.logLag(fetches)
		var handleErr error
		fetches.EachError(func(topic string, partition int32, err error) {
			if handleErr == nil {
				handleErr = fmt.Errorf("consume %s/%d: %w", topic, partition, err)
			}
		})
		fetches.EachRecord(func(r *kgo.Record) {
			if handleErr != nil {
				return
			}
			err := applyRecord(ctx, func() error {
				return handle(ctx, r.Topic, r.Value)
			}, func() error {
				return cl.CommitRecords(ctx, r)
			})
			if err != nil && ctx.Err() == nil {
				handleErr = err
			}
		})
		if handleErr != nil {
			return handleErr
		}
	}
}

// applyRecord commits only after handle succeeds. A failed publish leaves the
// record unacked so the broker delivers it again.
func applyRecord(ctx context.Context, handle func() error, commit func() error) error {
	if err := handle(); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (k *Kafka) logLag(fetches kgo.Fetches) {
	fetches.EachTopic(func(t kgo.FetchTopic) {
		if t.Topic != event.TopicRidesRequested {
			return
		}
		t.EachPartition(func(p kgo.FetchPartition) {
			if len(p.Records) == 0 {
				return
			}
			last := p.Records[len(p.Records)-1].Offset
			k.log.Info("kafka.consumer.lag", "topic", t.Topic, "partition", p.Partition, "lag", p.HighWatermark-last-1)
		})
	})
}

func splitSeeds(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
