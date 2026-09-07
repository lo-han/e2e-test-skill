package harness

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// GRPC holds the suite's connection to the service's gRPC interface.
//
// The stubs the scenarios call are generated into this suite's own module from
// the service's .proto by the suite's own toolchain — never imported from the
// service. A suite that imports the server's generated package agrees with the
// server by construction, which is exactly the agreement under test. Point
// Conn at your generated client:
//
//	client := pb.NewPaymentsClient(g.Conn)
type GRPC struct {
	Target string
	Conn   *grpc.ClientConn
}

// DialGRPC connects and proves something is actually answering.
//
// A plain dial proves nothing: gRPC connects lazily, so it succeeds against a
// port with nothing behind it. This blocks until the connection is up.
func DialGRPC(ctx context.Context, target string, timeout time.Duration) (*GRPC, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := grpc.DialContext(dialCtx, target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("connecting to grpc target %s: %w", target, err)
	}
	return &GRPC{Target: target, Conn: conn}, nil
}

// Close releases the connection.
func (g *GRPC) Close() error { return g.Conn.Close() }

// Ready is the readiness probe to hand to Service.Ready: the standard health
// service where the service registers it. When it does not, use a real unary
// call of your own instead.
func (g *GRPC) Ready(service string) func(context.Context) error {
	return func(ctx context.Context) error {
		client := healthpb.NewHealthClient(g.Conn)
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		if err != nil {
			return err
		}
		if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return fmt.Errorf("health check reports %s", resp.GetStatus())
		}
		return nil
	}
}

// WithMarker returns a context carrying the receipt marker in metadata, since
// a protobuf message has no room for a field its schema does not define.
func (g *GRPC) WithMarker(ctx context.Context, scenario string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MarkerField, scenario)
}

// WithMetadata returns a context carrying arbitrary metadata, for the
// scenarios asserting what the service does with required headers.
func (g *GRPC) WithMetadata(ctx context.Context, pairs ...string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, pairs...)
}

// Deadline returns a context that expires sooner than the work, so the
// scenario can assert the server observes cancellation, leaves no half-written
// record, and logs it.
func Deadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, d)
}

// Code is the gRPC status code of an error, for asserting the code the spec
// names rather than matching on message text.
//
//	assert.Equal(t, codes.InvalidArgument, harness.Code(err))
func Code(err error) codes.Code {
	return status.Code(err)
}

// StatusOf returns the full status, so a scenario can assert the message and
// the typed details the contract promises.
func StatusOf(err error) *status.Status {
	s, _ := status.FromError(err)
	return s
}

// Details returns the typed error details attached to err, which is where a
// contract that promises structured errors puts them.
func Details(err error) []any {
	s, ok := status.FromError(err)
	if !ok {
		return nil
	}
	return s.Details()
}

// CallUnknownMethod invokes a method that does not exist on the server — the
// gRPC equivalent of "a route one segment too long". It must answer
// Unimplemented, and a server that answers anything else is a finding.
func (g *GRPC) CallUnknownMethod(ctx context.Context, fullMethod string) error {
	var out, in struct{}
	return g.Conn.Invoke(ctx, fullMethod, &in, &out)
}
