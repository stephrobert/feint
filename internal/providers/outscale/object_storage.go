package outscale

import (
	"github.com/stephrobert/feint/internal/s3"
)

// ObjectStorage declares the OOS door, and declares only what was measured.
//
// ## Why this door exists at all
//
// #850 asks for an OUTSCALE or OOS client path "if the current official
// tooling exposes it", and the honest answer was assumed to be no for longer
// than it should have been. Measured 2026-11-09, five readings, none of them
// from documentation:
//
//	octl v0.0.32 carries `octl storage` ("OUTSCALE Object Storage (OOS)
//	  management") with bucket, object, multipartupload and part subcommands
//	`octl storage api` lists S3's own operation names — GetObject, PutObject,
//	  ListObjectsV2, CreateMultipartUpload — so OOS is S3, not a named API
//	osc-sdk-go pkg/oos/client.go is built on
//	  github.com/aws/aws-sdk-go-v2/service/s3, with `type Error = smithy.APIError`,
//	  so there is no proprietary error dialect to invent
//	pkg/profile/profile.go:114 reads OSC_ENDPOINT_OOS, and
//	  pkg/profile/endpoint.go:12 accepts an `oos` key in the profile file, so the
//	  endpoint is redirectable WITHOUT TLS interception, a fork or dev_overrides
//	pkg/profile/endpoint.go:27 gives the default authority, %s://oos.%s.outscale.com
//
// So the door is not simulated behind a false promise, which is the alternative
// #850 explicitly refuses. It is redirected the same way octl.sh already
// redirects this client's IaaS calls through OSC_ENDPOINT_API.
//
// ## What is NOT claimed here
//
// This declares the door, its endpoint variables and its measured missing-bucket
// envelope. It does not claim that octl's whole storage surface round-trips: no
// suite drives `octl storage` yet, and until one does, every operation of this
// door stays unproven by a real client whatever the S3 server underneath can
// serve. The conformance axis is the thing that says otherwise, and it says
// nothing yet.
//
// BucketConfiguration, ObjectDiscovery, ListObjectsV1 and PrivateACLWrite are
// deliberately left at their zero value rather than copied from the Scaleway
// profile next door. Each of those switches on an answer shape, and this door
// has one recording to its name. Copying a neighbour's switches would be
// inventing OOS's discovery behaviour from Scaleway's, which is the failure
// mode this pack's error helpers exist to avoid.
func (p *Pack) ObjectStorage(region string) s3.Profile {
	return s3.Profile{
		Provider:      Name,
		Region:        region,
		MissingBucket: s3.OutscaleMissingBucket,
		// Both names the SDK reads, in the order it reads them. OSC_ENDPOINT_OOS
		// is the per-service one (pkg/profile/profile.go:114); the profile file's
		// `oos` key wins over it when --config or --profile is given, which is
		// the same precedence octl.sh documents for the IaaS endpoint.
		Variables:      []string{"OSC_ENDPOINT_OOS"},
		RegionVariable: "OSC_REGION",
		// pkg/profile/endpoint.go:27, with its scheme placeholder resolved.
		CloudHost: "oos." + region + ".outscale.com",
	}
}
