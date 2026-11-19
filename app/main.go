package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	saDir    = "/var/run/secrets/kubernetes.io/serviceaccount"
	imdsBase = "http://169.254.169.254/latest"
)

// Field es un dato del entorno, en el orden en que se muestra.
type Field struct{ Key, Value string }

// ─────────────────────────────────────────────
// Datos del cluster/nodo (no cambian durante la vida del pod: se cachean)
// ─────────────────────────────────────────────

var (
	once    sync.Once
	envData []Field
)

func clusterFields() []Field {
	once.Do(func() {
		var version, providerID, nodegroup string
		var ec2 map[string]string
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); version, providerID, nodegroup = fromK8sAPI() }()
		go func() { defer wg.Done(); ec2 = fromIMDS() }()
		wg.Wait()

		envData = []Field{
			{"pod", orDefault(os.Getenv("POD_NAME"), hostname())},
			{"node", os.Getenv("NODE_NAME")},
			{"k8s_version", version},
			{"provider_id", providerID},
			{"nodegroup", nodegroup},
			{"instance_id", ec2["instance-id"]},
			{"instance_type", ec2["instance-type"]},
			{"az", ec2["placement/availability-zone"]},
			{"region", ec2["placement/region"]},
			// Evidencia de la entrega: los inyecta el pipeline en el Deployment
			{"github_user", os.Getenv("GITHUB_USER")},
			{"commit", os.Getenv("GIT_SHA")},
		}
	})
	return envData
}

// API de Kubernetes con el token de la ServiceAccount del pod.
func fromK8sAPI() (version, providerID, nodegroup string) {
	ca, err := os.ReadFile(saDir + "/ca.crt")
	if err != nil {
		return
	}
	token, _ := os.ReadFile(saDir + "/token")
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
	get := func(path string, out any) {
		url := "https://" + os.Getenv("KUBERNETES_SERVICE_HOST") + ":" + os.Getenv("KUBERNETES_SERVICE_PORT") + path
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		resp, err := client.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			json.NewDecoder(resp.Body).Decode(out)
		}
	}

	var v struct {
		GitVersion string `json:"gitVersion"` // en EKS: v1.35.x-eks-xxxxxxx
	}
	get("/version", &v)

	var n struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			ProviderID string `json:"providerID"` // aws:///<az>/<instance-id>
		} `json:"spec"`
	}
	if node := os.Getenv("NODE_NAME"); node != "" {
		get("/api/v1/nodes/"+node, &n)
	}
	return v.GitVersion, n.Spec.ProviderID, n.Metadata.Labels["eks.amazonaws.com/nodegroup"]
}

// IMDSv2 del EC2 (los nodos tienen hop limit 2). Solo datos de identidad.
func fromIMDS() map[string]string {
	out := map[string]string{}
	client := &http.Client{Timeout: time.Second}

	req, _ := http.NewRequest(http.MethodPut, imdsBase+"/api/token", nil)
	req.Header.Set("X-aws-ec2-metadata-token-ttl-seconds", "60")
	resp, err := client.Do(req)
	if err != nil {
		return out
	}
	token, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	for _, p := range []string{"instance-id", "instance-type", "placement/availability-zone", "placement/region"} {
		req, _ := http.NewRequest(http.MethodGet, imdsBase+"/meta-data/"+p, nil)
		req.Header.Set("X-aws-ec2-metadata-token", string(token))
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			out[p] = string(b)
		}
	}
	return out
}

func hostname() string { h, _ := os.Hostname(); return h }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ─────────────────────────────────────────────
// HTTP
// ─────────────────────────────────────────────

var page = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="es"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Hello from EKS</title>
<style>
  body { margin:0; background:#0f1419; color:#e6e9ec; font:15px/1.5 system-ui,sans-serif }
  main { max-width:640px; margin:0 auto; padding:40px 16px }
  h1 { margin:0 0 24px; font-size:28px } h1 span { color:#ff9900 }
  table { width:100%; border-collapse:collapse; background:#171d24; border:1px solid #26303b; border-radius:10px }
  td { padding:10px 16px; border-top:1px solid #26303b; font:14px ui-monospace,Menlo,monospace; word-break:break-all }
  tr:first-child td { border-top:0 }
  td:first-child { color:#8b98a5; width:35% }
</style></head>
<body><main>
  <h1>Hello from <span>EKS</span></h1>
  <table>{{range .}}<tr><td>{{.Key}}</td><td>{{if .Value}}{{.Value}}{{else}}-{{end}}</td></tr>{{end}}</table>
</main></body></html>`))

func main() {
	go clusterFields() // precalienta la cache

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		// El ALB agrega X-Amzn-Trace-Id a cada request que reenvia.
		fields := append(append([]Field{}, clusterFields()...),
			Field{"alb_trace_id", r.Header.Get("X-Amzn-Trace-Id")})

		switch {
		case r.URL.Query().Get("format") == "json":
			m := map[string]string{}
			for _, f := range fields {
				m[f.Key] = f.Value
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(m)
		case strings.Contains(r.Header.Get("Accept"), "text/html"):
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			page.Execute(w, fields)
		default: // curl: una linea key=value por dato, facil de grep/cut
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			for _, f := range fields {
				fmt.Fprintf(w, "%s=%s\n", f.Key, f.Value)
			}
		}
	})
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})

	log.Println("escuchando en :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
