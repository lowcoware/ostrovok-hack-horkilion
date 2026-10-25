package platform

import (
	"context"

	"google.golang.org/grpc/metadata"
)

// OwnerMetadataKey carries the owner of a request between services. The gateway
// authenticates the user and sets it; internal services trust it.
const OwnerMetadataKey = "x-owner-id"

// WithOwner returns an outgoing gRPC context carrying the owner.
func WithOwner(ctx context.Context, ownerID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, OwnerMetadataKey, ownerID)
}

// OwnerFrom returns the owner from incoming gRPC metadata, empty when absent.
func OwnerFrom(ctx context.Context) string {
	if v := metadata.ValueFromIncomingContext(ctx, OwnerMetadataKey); len(v) > 0 {
		return v[0]
	}
	return ""
}
