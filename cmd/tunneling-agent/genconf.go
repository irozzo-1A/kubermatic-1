/*
Copyright 2020 The Kubermatic Kubernetes Platform contributors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"text/template"
)

var envoyConfigTemplate = `admin:
  access_log_path: /dev/stdout
  address:
    socket_address:
      protocol: TCP
      address: 127.0.0.1
      port_value: {{.AdminPort}}
static_resources:
  listeners: {{if not .Listeners -}}[]{{- end}}
{{- range $i, $l := .Listeners}}
  - name: listener_{{$i}}
    address:
      socket_address:
        protocol: TCP
        address: {{$.BindAddress}}
        port_value: {{$l.BindPort}}
    filter_chains:
    - filters:
      - name: tcp
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy
          stat_prefix: tcp_stats
          cluster: "proxy_cluster"
          tunneling_config:
            hostname: {{$l.Authority}}
{{- end}}
  clusters:
    - name: proxy_cluster
      connect_timeout: 5s
      # This ensures HTTP/2 CONNECT is used for establishing the tunnel.
      typed_extension_protocol_options:
        envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
          "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
          explicit_http_config:
            http2_protocol_options: {}
      load_assignment:
        cluster_name: proxy_cluster
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address:
                      address: {{.ProxyHost}}
                      port_value: {{.ProxyPort}}
`

type GenconfOptions struct {
	AdminPort   uint
	BindAddress string
	ProxyHost   string
	ProxyPort   uint
	Listeners   Listeners
}

type GenconfCommand struct {
	GenconfOptions
	OutputWriter OutputWriter
}

type Listener struct {
	BindPort  uint
	Authority string
}

type Listeners []Listener

func (l *Listeners) String() string {
	return fmt.Sprintf("%+v", *l)
}

func (l *Listeners) Set(value string) error {
	if i := strings.IndexRune(value, ':'); i > 0 {
		var c Listener
		port, err := strconv.ParseUint(value[0:i], 10, 32)
		if err != nil {
			return fmt.Errorf("wrong format, expected port but got %q: %v", value[0:i], err)
		}
		c.BindPort = uint(port)
		auth := value[i+1 : len(value)]
		_, _, err = net.SplitHostPort(auth)
		if err != nil {
			return fmt.Errorf("wrong format, expected authority but got %q: %v", auth, err)
		}
		c.Authority = auth
		*l = append(*l, c)
		return nil
	}
	return fmt.Errorf("wrong format: separator ':' was not found in %q", value)
}

var _ io.Writer = &OutputWriter{}

type OutputWriter struct {
	file *os.File
}

func (w *OutputWriter) String() string {
	if w.file != nil {
		return w.file.Name()
	}
	return "stdout"
}

func (w *OutputWriter) Set(value string) error {
	if file, err := os.OpenFile(value, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644); err == nil {
		w.file = file
		return nil
	} else {
		return fmt.Errorf("error occurred while opening envoy config file: %v", err)
	}
}

func (w *OutputWriter) Write(p []byte) (n int, err error) {
	if w.file != nil {
		return w.file.Write(p)
	}
	return os.Stdout.Write(p)
}

func (c *GenconfCommand) Flags() *flag.FlagSet {
	genconfCmd := flag.NewFlagSet(c.String(), flag.ExitOnError)
	genconfCmd.UintVar(&c.AdminPort, "admin-port", 9903, "Admin port. Defaults to 9903.")
	genconfCmd.StringVar(&c.BindAddress, "bind-address", "0.0.0.0", "Bind address.")
	genconfCmd.StringVar(&c.ProxyHost, "proxy-host", "", "Proxy host.")
	genconfCmd.UintVar(&c.ProxyPort, "proxy-port", 0, "Proxy host.")
	genconfCmd.Var(&c.Listeners, "listener", "Listener configuration, this flag can be repeated. The expected format is: <local-port>:<authority>")
	genconfCmd.Var(&c.OutputWriter, "out", "Output file used to write the Envoy configuration, stdout is used if unpecified.")
	return genconfCmd
}

func (c *GenconfCommand) Exec() error {

	t := template.Must(template.New("envoy-config").Parse(envoyConfigTemplate))
	return t.Execute(&c.OutputWriter, c.GenconfOptions)
}

func (_ *GenconfCommand) String() string {
	return "genconf"
}
