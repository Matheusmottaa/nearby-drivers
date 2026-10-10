package driver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/Matheusmottaa/nearby-drivers/internal/event"
)

// ReplayWindow is the drivers.location retention read on boot.
const ReplayWindow = 5 * time.Minute

// ReservationGroup is the single consumer group of this process.
// rides.assigned and rides.released are the only topics it follows.
const ReservationGroup = "driver-service"

// Kafka publishes driver events, replays drivers.location and consumes reservations.
// cl is the process producer. Replay and the reservation consumer open their own clients.
type Kafka struct {
	cl    *kgo.Client
	seeds []string
}

// NewKafka returns a bus. seeds is the same broker list passed to depend.OpenKafka.
func NewKafka(cl *kgo.Client, brokers string) *Kafka {
	return &Kafka{cl: cl, seeds: splitSeeds(brokers)}
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

// ReplayLocations reads drivers.location from the start of the retention window
// up to the end offset sampled before the read. The client does not join a
// group and does not commit, so the next boot reads the window again.
func (k *Kafka) ReplayLocations(ctx context.Context, apply func(event.Location) error) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(k.seeds...),
		kgo.DialTimeout(5*time.Second),
		kgo.FetchMaxWait(500*time.Millisecond),
	)
	if err != nil {
		return fmt.Errorf("replay client: %w", err)
	}
	defer cl.Close()

	partitions, err := topicPartitions(ctx, cl, event.TopicDriversLocation)
	if err != nil {
		return err
	}
	since := time.Now().Add(-ReplayWindow).UnixMilli()
	start, err := listOffsets(ctx, cl, event.TopicDriversLocation, partitions, since)
	if err != nil {
		return err
	}
	end, err := listOffsets(ctx, cl, event.TopicDriversLocation, partitions, -1)
	if err != nil {
		return err
	}

	want := make(map[int32]int64)
	assign := make(map[int32]kgo.Offset)
	for _, p := range partitions {
		if !replaySpan(start[p], end[p]) {
			continue
		}
		want[p] = end[p]
		assign[p] = kgo.NewOffset().At(start[p])
	}
	if len(want) == 0 {
		return nil
	}
	cl.AddConsumePartitions(map[string]map[int32]kgo.Offset{
		event.TopicDriversLocation: assign,
	})

	for len(want) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		fetches := cl.PollFetches(ctx)
		if err := ctx.Err(); err != nil {
			return err
		}
		var ferr error
		fetches.EachError(func(topic string, partition int32, err error) {
			if ferr == nil {
				ferr = fmt.Errorf("replay %s/%d: %w", topic, partition, err)
			}
		})
		fetches.EachRecord(func(r *kgo.Record) {
			high, ok := want[r.Partition]
			if !ok {
				return
			}
			loc, decErr := event.Decode[event.Location](r.Value)
			if decErr == nil {
				if err := apply(loc); err != nil && ferr == nil {
					ferr = err
				}
			}
			if r.Offset+1 >= high {
				delete(want, r.Partition)
			}
		})
		if ferr != nil {
			return ferr
		}
	}
	return nil
}

// ConsumeReservations reads rides.assigned and rides.released in ReservationGroup.
// A record is committed after handle returns nil. It returns when ctx is canceled.
func (k *Kafka) ConsumeReservations(ctx context.Context, handle func(context.Context, string, []byte) error) error {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(k.seeds...),
		kgo.ConsumerGroup(ReservationGroup),
		kgo.ConsumeTopics(event.TopicRidesAssigned, event.TopicRidesReleased),
		kgo.DisableAutoCommit(),
		kgo.DialTimeout(5*time.Second),
	)
	if err != nil {
		return fmt.Errorf("reservation consumer: %w", err)
	}
	defer cl.Close()

	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if fetches.IsClientClosed() {
			return fmt.Errorf("reservation consumer closed")
		}
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
			if err := handle(ctx, r.Topic, r.Value); err != nil {
				handleErr = err
				return
			}
			if err := cl.CommitRecords(ctx, r); err != nil && ctx.Err() == nil {
				handleErr = fmt.Errorf("commit %s/%d: %w", r.Topic, r.Partition, err)
			}
		})
		if handleErr != nil {
			return handleErr
		}
	}
}

// replaySpan reports whether [start, end) contains a record to read.
// end is the log end offset. start -1 means nothing in the time window.
func replaySpan(start, end int64) bool {
	return start >= 0 && start < end
}

func topicPartitions(ctx context.Context, cl *kgo.Client, topic string) ([]int32, error) {
	req := kmsg.NewMetadataRequest()
	req.Version = 9
	req.AllowAutoTopicCreation = false
	name := topic
	req.Topics = []kmsg.MetadataRequestTopic{{Topic: &name}}
	resp, err := req.RequestWith(ctx, cl)
	if err != nil {
		return nil, fmt.Errorf("metadata %s: %w", topic, err)
	}
	if len(resp.Topics) == 0 {
		return nil, fmt.Errorf("metadata %s: empty response", topic)
	}
	t := resp.Topics[0]
	if err := kerr.ErrorForCode(t.ErrorCode); err != nil {
		return nil, fmt.Errorf("metadata %s: %w", topic, err)
	}
	ids := make([]int32, 0, len(t.Partitions))
	for _, p := range t.Partitions {
		if err := kerr.ErrorForCode(p.ErrorCode); err != nil {
			return nil, fmt.Errorf("metadata %s/%d: %w", topic, p.Partition, err)
		}
		ids = append(ids, p.Partition)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("metadata %s: no partitions", topic)
	}
	return ids, nil
}

func listOffsets(ctx context.Context, cl *kgo.Client, topic string, partitions []int32, timestamp int64) (map[int32]int64, error) {
	req := kmsg.NewListOffsetsRequest()
	req.Version = 7
	part := kmsg.NewListOffsetsRequestTopic()
	part.Topic = topic
	for _, p := range partitions {
		item := kmsg.NewListOffsetsRequestTopicPartition()
		item.Partition = p
		item.Timestamp = timestamp
		part.Partitions = append(part.Partitions, item)
	}
	req.Topics = []kmsg.ListOffsetsRequestTopic{part}
	resp, err := req.RequestWith(ctx, cl)
	if err != nil {
		return nil, fmt.Errorf("list offsets %s: %w", topic, err)
	}
	out := make(map[int32]int64, len(partitions))
	for _, t := range resp.Topics {
		for _, p := range t.Partitions {
			if err := kerr.ErrorForCode(p.ErrorCode); err != nil {
				return nil, fmt.Errorf("list offsets %s/%d: %w", topic, p.Partition, err)
			}
			out[p.Partition] = p.Offset
		}
	}
	return out, nil
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
