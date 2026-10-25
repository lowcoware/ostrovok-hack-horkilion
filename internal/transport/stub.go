// Package transport implements transportv1.TransportService providers.
package transport

import transportv1 "horkilion/gen/transport/v1"

// Stub is the provider "stub": every RPC returns UNIMPLEMENTED until flights
// and trains arrive in phase 1.
type Stub struct {
	transportv1.UnimplementedTransportServiceServer
}
