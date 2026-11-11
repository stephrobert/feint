package scaleway

import (
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/s3"
)

// ObjectStorage declares the measured S3 dialect and configurable CLI endpoint.
// It enables measured private, non-versioned CLI and Terraform discovery,
// not public permissions, writable tags or object version histories.
func (p *Pack) ObjectStorage(region string) s3.Profile {
	owner := emulator.Project{Name: defaultProjectName}
	if p.env != nil && len(p.env.Projects) != 0 {
		owner = p.env.Projects[0]
	}
	return s3.Profile{Provider: Name, Region: region, MissingBucket: s3.ScalewayMissingBucket,
		Variables: []string{"SCW_S3_ENDPOINT"}, RegionVariable: "SCW_DEFAULT_REGION",
		CloudHost:           "s3." + region + ".scw.cloud",
		BucketConfiguration: true, ObjectDiscovery: true, ListObjectsV1: true, OwnerID: identifierOf(owner), PrivateACLWrite: true,
		AbsentConfigurations: map[string]s3.AbsentConfiguration{
			"cors":        {Status: 404, Code: "NoSuchCORSConfiguration", Message: "The CORS configuration does not exist", RequestIDs: true, Resource: true},
			"object-lock": {Status: 404, Code: "ObjectLockConfigurationNotFoundError", Message: "Object Lock configuration does not exist for this bucket", RequestIDs: true, Resource: true},
			"lifecycle":   {Status: 404, Code: "NoSuchLifecycleConfiguration", Message: "The lifecycle configuration does not exist", RequestIDs: true, Resource: true, BucketName: true},
		}}
}
