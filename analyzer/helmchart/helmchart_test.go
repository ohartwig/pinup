// SPDX-FileCopyrightText: 2026 Kai Ole Hartwig <mail@ole-hartwig.eu>
// SPDX-License-Identifier: Apache-2.0

package helmchart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ohartwig/pinup/classify"
	"github.com/ohartwig/pinup/fake/harness"
	"github.com/ohartwig/pinup/httpx"
	"github.com/ohartwig/pinup/model"
)

// archive builds a chart archive as `helm package` lays it out: the
// chart's name as the top directory, Chart.yaml and values.yaml in it,
// and a subchart's files under charts/, which must not be read as the
// chart's own.
func archive(t *testing.T, name, chartYAML, valuesYAML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for path, body := range map[string]string{
		name + "/Chart.yaml":                chartYAML,
		name + "/values.yaml":               valuesYAML,
		name + "/charts/common/Chart.yaml":  "apiVersion: v2\nname: common\nversion: 2.0.0\nappVersion: 99.0.0\n",
		name + "/charts/common/values.yaml": "exampleValue: gone\n",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: path, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

const oldChart = `apiVersion: v2
name: redis
version: 20.13.4
appVersion: 7.4.2
kubeVersion: ">=1.23.0-0"
dependencies:
  - name: common
    version: 2.30.0
`

const oldValues = `image:
  registry: docker.io
  repository: bitnami/redis
  tag: 7.4.2
auth:
  enabled: true
  password: ""
master:
  replicaCount: 1
  service:
    ports:
      redis: 6379
sentinel:
  enabled: false
`

// repo is a Helm repository: index.yaml and the archives it names, one
// relative and one absolute, as real indexes mix them.
type repo struct {
	index    string
	archives map[string][]byte
}

func (r *repo) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.URL.Path == "/charts/index.yaml":
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write([]byte(r.index))
	case strings.HasSuffix(req.URL.Path, ".tgz"):
		b, ok := r.archives[req.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(b)
	default:
		w.WriteHeader(404)
	}
}

func newClient(t *testing.T, hosts map[string]http.Handler) (*httpx.Client, *harness.Recorder) {
	t.Helper()
	rec := &harness.Recorder{}
	rt := harness.NewRefusingTransport(rec)
	for h, handler := range hosts {
		rt.Handle(h, handler)
	}
	return httpx.New(httpx.Options{Transport: rt, Now: func() time.Time { return time.Unix(0, 0) }, Sleep: func(time.Duration) {}}), rec
}

func TestAMajorChartWhoseAppAndValuesStoodStillIsAPatch(t *testing.T) {
	newChart := strings.NewReplacer("version: 20.13.4", "version: 21.0.0", "version: 2.30.0", "version: 2.31.0").Replace(oldChart)
	r := &repo{
		index: `apiVersion: v1
entries:
  redis:
  - version: 21.0.0
    appVersion: 7.4.2
    urls: [redis-21.0.0.tgz]
  - version: 20.13.4
    appVersion: 7.4.2
    urls: [https://charts.example/charts/redis-20.13.4.tgz]
`,
		archives: map[string][]byte{
			"/charts/redis-20.13.4.tgz": archive(t, "redis", oldChart, oldValues),
			"/charts/redis-21.0.0.tgz":  archive(t, "redis", newChart, oldValues+"metrics:\n  enabled: false\n"),
		},
	}
	client, rec := newClient(t, map[string]http.Handler{"charts.example": r})
	a := New(client, nil)
	dep := model.Dependency{DepName: "redis", Datasource: "helm", RegistryURLs: []string{"https://charts.example/charts"}}
	if !a.Applies(dep) {
		t.Fatal("a helm dependency with a repository applies")
	}
	e, err := a.Analyze(context.Background(), dep, "20.13.4", "21.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Failed() {
		t.Fatalf("the transport was unhappy: %s", rec.String())
	}
	if e.Risk != model.RiskPatch {
		t.Errorf("risk = %s, want patch: the application stood still and no value was removed\n%+v", e.Risk, e.Evidence)
	}
	kinds := map[string]model.Evidence{}
	for _, ev := range e.Evidence {
		kinds[ev.Kind+"|"+ev.Note] = ev
	}
	if _, ok := kinds["appVersion|unchanged"]; !ok {
		t.Errorf("appVersion evidence missing: %+v", e.Evidence)
	}
	if _, ok := kinds["values|0 keys removed, 1 added"]; !ok {
		t.Errorf("values evidence: %+v", e.Evidence)
	}
	if _, ok := kinds["dependencies|common"]; !ok {
		t.Errorf("the subchart's move is evidence: %+v", e.Evidence)
	}
}

func TestARemovedValueIsBreakingWhateverTheAppDid(t *testing.T) {
	newValues := strings.Replace(oldValues, "auth:\n  enabled: true\n  password: \"\"\n", "auth:\n  enabled: true\n", 1)
	newChart := strings.NewReplacer("version: 20.13.4", "version: 20.14.0", "appVersion: 7.4.2", "appVersion: 7.4.3").Replace(oldChart)
	r := &repo{
		index: "apiVersion: v1\nentries:\n  redis:\n  - version: 20.14.0\n    urls: [redis-20.14.0.tgz]\n  - version: 20.13.4\n    urls: [redis-20.13.4.tgz]\n",
		archives: map[string][]byte{
			"/charts/redis-20.13.4.tgz": archive(t, "redis", oldChart, oldValues),
			"/charts/redis-20.14.0.tgz": archive(t, "redis", newChart, newValues),
		},
	}
	client, _ := newClient(t, map[string]http.Handler{"charts.example": r})
	e, err := New(client, nil).Analyze(context.Background(), model.Dependency{DepName: "redis", Datasource: "helm", RegistryURLs: []string{"https://charts.example/charts/"}}, "20.13.4", "20.14.0")
	if err != nil {
		t.Fatal(err)
	}
	if e.Risk != model.RiskBreakingValues {
		t.Errorf("risk = %s, want breaking-values", e.Risk)
	}
	found := false
	for _, ev := range e.Evidence {
		if ev.Kind == "values" && strings.Contains(ev.Note, "1 keys removed") && strings.Contains(ev.Note, "auth.password") {
			found = true
		}
	}
	if !found {
		t.Errorf("the removed path must be named: %+v", e.Evidence)
	}
}

func TestTheAppVersionDecidesTheLabel(t *testing.T) {
	for _, c := range []struct {
		from, to string
		want     model.Risk
		note     string
	}{
		{"7.4.2", "8.0.0", model.RiskMajor, "major"},
		{"7.4.2", "7.5.0", model.RiskMinor, "minor"},
		{"7.4.2", "7.4.3", model.RiskPatch, "patch"},
		{"v1.28", "v1.29", model.RiskMinor, "minor"},
		{"8.0.0-debian-12-r3", "8.0.1-debian-12-r0", model.RiskPatch, "patch"},
		{"", "7.4.3", model.RiskUnknown, "not stated"},
		{"latest", "7.4.3", model.RiskUnknown, "not comparable"},
	} {
		e := compare(chart{appVersion: c.from}, chart{appVersion: c.to})
		if e.Risk != c.want || e.Evidence[0].Note != c.note {
			t.Errorf("%s -> %s: risk %s note %q, want %s %q", c.from, c.to, e.Risk, e.Evidence[0].Note, c.want, c.note)
		}
	}
}

// ociRegistry serves a chart as `helm push` stores it: a manifest whose
// config is the chart's metadata and whose layer is the archive.
type ociRegistry struct {
	charts map[string][]byte // tag -> archive
	image  map[string]bool   // tags that are images, not charts
}

func (o *ociRegistry) Manifest(_ context.Context, pkg string, _ []string, tag string) ([]byte, error) {
	if o.image[tag] {
		return []byte(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:0"},"layers":[]}`), nil
	}
	data, ok := o.charts[tag]
	if !ok {
		return nil, fmt.Errorf("manifest unknown: %s:%s", pkg, tag)
	}
	sum := sha256.Sum256(data)
	m := map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"mediaType": configMediaType, "digest": "sha256:cfg"},
		"layers":        []map[string]any{{"mediaType": chartMediaType, "digest": fmt.Sprintf("sha256:%x", sum), "size": len(data)}},
	}
	return json.Marshal(m)
}

func (o *ociRegistry) Blob(_ context.Context, pkg string, _ []string, digest string, _ int64) ([]byte, error) {
	for _, data := range o.charts {
		if sum := sha256.Sum256(data); fmt.Sprintf("sha256:%x", sum) == digest {
			return data, nil
		}
	}
	return nil, fmt.Errorf("blob unknown: %s@%s", pkg, digest)
}

func TestAnOCIChartIsReadFromItsManifestAndAnImageIsNotOurs(t *testing.T) {
	newChart := strings.NewReplacer("version: 20.13.4", "version: 21.0.0", "appVersion: 7.4.2", "appVersion: 8.0.0").Replace(oldChart)
	reg := &ociRegistry{charts: map[string][]byte{
		"20.13.4": archive(t, "redis", oldChart, oldValues),
		"21.0.0":  archive(t, "redis", newChart, oldValues),
	}, image: map[string]bool{"7.4.2-debian-12-r0": true}}
	client, _ := newClient(t, nil)
	a := New(client, reg)
	dep := model.Dependency{DepName: "registry-1.docker.io/bitnamicharts/redis", Datasource: "docker"}
	if !a.Applies(dep) {
		t.Fatal("a docker dependency applies when an OCI reader is there")
	}
	e, err := a.Analyze(context.Background(), dep, "20.13.4", "21.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if e.Risk != model.RiskMajor {
		t.Errorf("risk = %s, want major (the application moved a major)", e.Risk)
	}
	_, err = a.Analyze(context.Background(), model.Dependency{DepName: "bitnami/redis", Datasource: "docker"}, "7.4.2-debian-12-r0", "7.4.2-debian-12-r0")
	if err != classify.ErrNotApplicable {
		t.Errorf("an image is not a chart: err = %v", err)
	}
	if New(client, nil).Applies(dep) {
		t.Error("without an OCI reader a docker dependency does not apply")
	}
}

func TestRegistryRunSkipsWhatDoesNotApply(t *testing.T) {
	reg := &ociRegistry{image: map[string]bool{"x": true}}
	client, _ := newClient(t, nil)
	r := classify.Registry{New(client, reg)}
	_, name, ok, err := r.Run(context.Background(), model.Dependency{DepName: "lib/nginx", Datasource: "docker"}, "x", "x")
	if err != nil || ok || name != "" {
		t.Errorf("an image answered by nobody: %v %q %v", ok, name, err)
	}
}
