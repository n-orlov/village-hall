package repocheck

// The Image unit builds the root Dockerfile once per architecture (SPEC 15.3):
// ci/image-build.sh builds it, ci/image-check.sh inspects it, and the release dry
// run (ci/release-check.sh -> ci/release-build.sh) reuses that unit's OCI tar and
// docker-save tar instead of calling ci/image-build.sh again. These tests run the
// real scripts in a scratch checkout with stub docker, go, trivy, npm, gotestsum
// and ci/run.sh that record every call, and count the image builds.

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const dryRunVersion = "v0.0.0-dryrun"

var imageOnceStubs = map[string]string{
	"docker": "#!/bin/sh\necho \"docker $*\" >> \"$STUB_LOG\"\n",
	"npm":    "#!/bin/sh\necho \"npm $*\" >> \"$STUB_LOG\"\n",
	"go": `#!/bin/sh
echo "go $*" >> "$STUB_LOG"
while [ $# -gt 0 ]; do
	if [ "$1" = -o ]; then mkdir -p "$(dirname "$2")" && echo vh > "$2"; fi
	shift
done
`,
	"trivy": `#!/bin/sh
echo "trivy $*" >> "$STUB_LOG"
while [ $# -gt 0 ]; do
	if [ "$1" = --output ]; then echo '{"bomFormat":"CycloneDX"}' > "$2"; fi
	shift
done
`,
	"gotestsum": `#!/bin/sh
echo "gotestsum $*" >> "$STUB_LOG"
while [ $# -gt 0 ]; do
	if [ "$1" = --junitfile ]; then echo '<testsuites/>' > "$2"; fi
	shift
done
cp "${BOARD_RELEASE_OUT#/w/}/image/board-image-oci.tar" "$STUB_KEEP/oci.tar"
`,
}

const (
	// ci/run.sh runs the command on the host, with /tmp/sbom moved into the scratch dir.
	imageOnceRunSh = `#!/bin/sh
echo "run.sh $*" >> "$STUB_LOG"
if [ "$1" = sh ] && [ "$2" = -c ]; then
	exec sh -c "$(printf '%s' "$3" | sed "s#/tmp/sbom#$STUB_TMP/sbom#g")"
fi
exec "$@"
`
	// ci/image-build.sh is the build being counted.
	imageOnceImageBuild = `#!/bin/sh
echo "image-build $*" >> "$STUB_LOG"
mkdir -p "$(dirname "$1")"
echo rebuilt > "$1"
echo rebuilt > "$(dirname "$1")/board-image-oci.tar"
`
)

// ociFixture is an OCI layout like ci/image-build.sh's: an index, a layout marker
// and blobs, the index carrying no image name.
func ociFixture(t *testing.T) (raw []byte, blobs map[string]string) {
	t.Helper()
	blobs = map[string]string{
		"blobs/sha256/aaaa": "the amd64 manifest",
		"blobs/sha256/bbbb": "the arm64 manifest",
		"blobs/sha256/cccc": "a layer",
	}
	files := map[string]string{
		"oci-layout": `{"imageLayoutVersion":"1.0.0"}`,
		"index.json": `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json","digest":"sha256:aaaa","size":645,"annotations":{"org.opencontainers.image.created":"2026-11-21T15:16:24Z"}}]}`,
	}
	for k, v := range blobs {
		files[k] = v
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), blobs
}

// ociFiles is a layout tar's regular files by name.
func ociFiles(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	files := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatalf("the release OCI layout is not a tar: %v", err)
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[strings.TrimPrefix(h.Name, "./")] = string(body)
	}
}

// imageOnceRun is one run of release-check.sh (or release-build.sh) in a scratch
// checkout whose ci-results holds the Image unit's two tars.
type imageOnceRun struct {
	dir, log, keep string
	blobs          map[string]string
	stdout, stderr string
	err            error
}

func runImageOnce(t *testing.T, buildScript, checkScript string, withTars bool, entry string, args ...string) *imageOnceRun {
	t.Helper()
	dir := t.TempDir()
	r := &imageOnceRun{dir: dir, log: filepath.Join(dir, "calls.log"), keep: filepath.Join(dir, "keep")}
	bin := filepath.Join(dir, "bin")
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range imageOnceStubs {
		write("bin/"+name, body, 0o755)
	}
	for _, d := range []string{"keep", "web/node_modules", "ci-results"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("ci/run.sh", imageOnceRunSh, 0o755)
	write("ci/image-build.sh", imageOnceImageBuild, 0o755)
	write("ci/release-build.sh", buildScript, 0o755)
	write("ci/release-check.sh", checkScript, 0o755)
	write("ci/oci-name.sh", readRepoFile(t, "ci", "oci-name.sh"), 0o755)
	write("install.sh", "#!/bin/sh\n", 0o755)
	write("INSTALL-AGENT.md", "# agent\n", 0o644)
	var oci []byte
	oci, r.blobs = ociFixture(t)
	if withTars {
		write("ci-results/board-image-oci.tar", string(oci), 0o644)
		write("ci-results/board-image.tar", "the amd64 docker save", 0o644)
	}
	cmd := exec.Command("sh", append([]string{filepath.Join(dir, "ci", entry)}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "TMPDIR=" + dir,
		"STUB_LOG=" + r.log, "STUB_TMP=" + dir, "STUB_KEEP=" + r.keep,
		"BOARD_IMAGE_REVISION=0123456789abcdef0123456789abcdef01234567",
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	r.err = cmd.Run()
	r.stdout, r.stderr = so.String(), se.String()
	return r
}

// calls are the recorded stub calls whose line starts with prefix.
func (r *imageOnceRun) calls(prefix string) []string {
	raw, err := os.ReadFile(r.log)
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.HasPrefix(l, prefix) {
			out = append(out, l)
		}
	}
	return out
}

func (r *imageOnceRun) failure() string {
	return "stdout:\n" + r.stdout + "\nstderr:\n" + r.stderr
}

// dryRunBuildProblems runs the dry run (ci/release-check.sh) against the given
// scripts, with the Image unit's tars in place, and reports whatever means the
// image was built again or the unit's image was not the one used.
func dryRunBuildProblems(t *testing.T, buildScript, checkScript string) []string {
	t.Helper()
	r := runImageOnce(t, buildScript, checkScript, true, "release-check.sh", "ci-results")
	var problems []string
	if r.err != nil {
		problems = append(problems, "the dry run failed: "+r.err.Error()+"\n"+r.failure())
	}
	if n := len(r.calls("image-build")); n != 0 {
		problems = append(problems, "the dry run called ci/image-build.sh again")
	}
	for _, c := range r.calls("docker ") {
		if strings.Contains(c, "buildx") {
			problems = append(problems, "the dry run built an image: "+c)
		}
	}
	scan := r.calls("trivy image")
	if len(scan) != 1 || !strings.Contains(scan[0], "--input ci-results/board-image.tar ") {
		problems = append(problems, "the image SBOM does not scan the unit's board-image.tar: "+strings.Join(scan, "; "))
	}
	return problems
}

func TestReleaseDryRunReusesTheImageUnitsTarAndBuildsNoImage(t *testing.T) {
	build, check := readRepoFile(t, "ci", "release-build.sh"), readRepoFile(t, "ci", "release-check.sh")
	for _, p := range dryRunBuildProblems(t, build, check) {
		t.Error(p)
	}
}

// The layout the release ships is the unit's image: every blob byte for byte,
// the index only gaining the name village-hall:<version> the release test reads.
func TestReleaseDryRunShipsTheUnitsOCILayoutNamedVillageHall(t *testing.T) {
	build, check := readRepoFile(t, "ci", "release-build.sh"), readRepoFile(t, "ci", "release-check.sh")
	r := runImageOnce(t, build, check, true, "release-check.sh", "ci-results")
	if r.err != nil {
		t.Fatalf("the dry run failed: %v\n%s", r.err, r.failure())
	}
	raw, err := os.ReadFile(filepath.Join(r.keep, "oci.tar"))
	if err != nil {
		t.Fatalf("the dry run left no OCI layout for the release test: %v", err)
	}
	files := ociFiles(t, raw)
	for name, body := range r.blobs {
		if files[name] != body {
			t.Errorf("%s is %q in the release layout, want the unit's %q", name, files[name], body)
		}
	}
	var top struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal([]byte(files["index.json"]), &top); err != nil || len(top.Manifests) != 1 {
		t.Fatalf("index.json: %v, %d manifests\n%s", err, len(top.Manifests), files["index.json"])
	}
	m := top.Manifests[0]
	if m.Digest != "sha256:aaaa" || m.Annotations["org.opencontainers.image.created"] == "" {
		t.Errorf("the index changed beyond its name: %+v", m)
	}
	if got, want := m.Annotations["io.containerd.image.name"], "docker.io/library/village-hall:"+dryRunVersion; got != want {
		t.Errorf("io.containerd.image.name = %q, want %q", got, want)
	}
	if got := m.Annotations["org.opencontainers.image.ref.name"]; got != dryRunVersion {
		t.Errorf("org.opencontainers.image.ref.name = %q, want %q", got, dryRunVersion)
	}
}

// A real release has no unit image to reuse: it builds, once.
func TestReleaseBuildWithoutAnImageDirBuildsTheImageOnce(t *testing.T) {
	build, check := readRepoFile(t, "ci", "release-build.sh"), readRepoFile(t, "ci", "release-check.sh")
	r := runImageOnce(t, build, check, false, "release-build.sh", dryRunVersion, "out")
	if r.err != nil {
		t.Fatalf("the release build failed: %v\n%s", r.err, r.failure())
	}
	if n := len(r.calls("image-build")); n != 1 {
		t.Errorf("a release builds the image %d times, want once", n)
	}
}

func TestReleaseDryRunStopsWithoutTheImageUnitsTars(t *testing.T) {
	build, check := readRepoFile(t, "ci", "release-build.sh"), readRepoFile(t, "ci", "release-check.sh")
	r := runImageOnce(t, build, check, false, "release-check.sh", "ci-results")
	if r.err == nil {
		t.Fatalf("the dry run succeeded without an image to check\n%s", r.failure())
	}
	if !strings.Contains(r.stderr, "builds no image of its own") {
		t.Errorf("the refusal does not say the dry run builds no image: %s", r.stderr)
	}
	if n := len(r.calls("image-build")); n != 0 {
		t.Errorf("the dry run built an image itself (%d calls)", n)
	}
}

// Doctored scripts that build a second time are caught: a release-build.sh that
// calls ci/image-build.sh on the reuse path too, and a release-check.sh that no
// longer hands the unit's image over.
func TestDoctoredReleaseScriptsThatBuildTheImageAgainAreCaught(t *testing.T) {
	build, check := readRepoFile(t, "ci", "release-build.sh"), readRepoFile(t, "ci", "release-check.sh")
	again := replaceOnce(t, build,
		`	image_tar="$prebuilt_rel/board-image.tar"`,
		"	ci/image-build.sh \"$rel/image/board-image.tar\"\n	image_tar=\"$prebuilt_rel/board-image.tar\"")
	cases := map[string][2]string{
		"a release-build.sh that builds on the reuse path too": {again, check},
		"a release-check.sh that hands over no image":          {build, replaceOnce(t, check, `BOARD_RELEASE_IMAGE_DIR=$dir ci/release-build.sh`, `ci/release-build.sh`)},
	}
	for name, scripts := range cases {
		if len(dryRunBuildProblems(t, scripts[0], scripts[1])) == 0 {
			t.Errorf("%s was not caught", name)
		}
	}
}

// imageUnitBuildsOnceProblems: the Jenkinsfile's Image unit calls
// ci/image-build.sh once and reaches the release dry run only through
// ci/release-check.sh (which reuses that image), never ci/release-build.sh.
func imageUnitBuildsOnceProblems(jf string) []string {
	core, _ := jenkinsStage(jf, "Core")
	i := strings.Index(core, "units << [name: 'Image'")
	if i < 0 {
		return []string{"Core has no Image unit"}
	}
	unit, _, _ := strings.Cut(core[i:], "units << [name: 'Allure smoke'")
	var problems []string
	if n := strings.Count(strings.Join(jenkinsfileLines(unit), "\n"), "ci/image-build.sh"); n != 1 {
		problems = append(problems, fmt.Sprintf("the Image unit calls ci/image-build.sh %d times, want once", n))
	}
	if strings.Contains(strings.Join(jenkinsfileLines(unit), "\n"), "ci/release-build.sh") {
		problems = append(problems, "the Image unit calls ci/release-build.sh itself, which would build the image again")
	}
	if !strings.Contains(unit, `ci/release-check.sh "$BOARD_CI_OUT"`) {
		problems = append(problems, "the Image unit's release dry run is not ci/release-check.sh over the unit's output directory")
	}
	return problems
}

func TestImageUnitBuildsTheImageOnce(t *testing.T) {
	jf := readRepoFile(t, "Jenkinsfile")
	expectProblems(t, jf, imageUnitBuildsOnceProblems, map[string]string{
		"an Image unit that builds twice": replaceOnce(t, jf,
			`script: 'ci/release-check.sh "$BOARD_CI_OUT"')`,
			`script: 'ci/image-build.sh "$BOARD_CI_OUT/board-image.tar" && ci/release-check.sh "$BOARD_CI_OUT"')`),
		"an Image unit whose dry run builds its own image": replaceOnce(t, jf,
			`script: 'ci/release-check.sh "$BOARD_CI_OUT"')`,
			`script: 'ci/release-build.sh v0.0.0-dryrun "$BOARD_CI_OUT/release-dryrun"')`),
	})
}
