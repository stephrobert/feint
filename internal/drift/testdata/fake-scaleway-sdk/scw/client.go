// Package scw stands in for the SDK's gateway package.
//
// Three methods, and the three are the distinction the gateway walk has to
// make. They are copied in shape from the real scw/client.go, where reusing the
// product walk's issuesRequest found Do and String and missed GetAPIMetadata.
package scw

// ScalewayRequest is the type a gateway call builds. Unqualified here, because
// this file lives in the package that declares it — which is exactly what the
// product walk's matcher cannot see.
type ScalewayRequest struct {
	Method string
	Path   string
}

// ApiMetadata is what the gateway answers.
type ApiMetadata struct {
	Platform  string
	Partition string
	Domain    string
}

// Client is the gateway client.
type Client struct {
	apiMetadata ApiMetadata
}

// GetAPIMetadata BUILDS a request, so it is an operation. This is the one the
// walk must find.
func (c *Client) GetAPIMetadata() (ApiMetadata, error) {
	scwReq := &ScalewayRequest{
		Method: "GET",
		Path:   "/metadata",
	}
	err := c.Do(scwReq, &c.apiMetadata)
	if err != nil {
		return ApiMetadata{}, err
	}
	return c.apiMetadata, nil
}

// Do RECEIVES a request and carries it. It is the transport every call goes
// through, and counting it would report the plumbing as an endpoint — which the
// product walk's matcher did.
func (c *Client) Do(req *ScalewayRequest, res any) error {
	_ = req
	_ = res
	return nil
}

// Config is configuration, and String formats it. It reaches nothing.
type Config struct {
	Name string
}

// String never sees a request, and was reported as an operation all the same.
func (c *Config) String() string {
	return c.Name
}
