// Package transfer implements transferv1.TransferService providers.
package transfer

import transferv1 "horkilion/gen/transfer/v1"

// Stub is the provider "stub": every RPC returns UNIMPLEMENTED until transfers
// arrive in phase 1.
type Stub struct {
	transferv1.UnimplementedTransferServiceServer
}
