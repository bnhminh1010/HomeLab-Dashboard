package homelab

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestFrontendHasNoNodeToolchainOrExternalLoads(t *testing.T) {
	for _, name := range []string{"package.json", "package-lock.json", "npm-shrinkwrap.json", "node_modules"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("forbidden Node/npm artifact exists: %s", name)
		}
	}
	assets, err := Static()
	if err != nil {
		t.Fatal(err)
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	references := regexp.MustCompile(`(?i)(?:src|href)=["']([^"']+)["']`).FindAllSubmatch(index, -1)
	for _, match := range references {
		value := string(match[1])
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "//") {
			t.Errorf("external browser asset reference: %s", value)
		}
	}
	err = fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(name, ".css") {
			return walkErr
		}
		contents, readErr := fs.ReadFile(assets, name)
		if readErr == nil && (bytes.Contains(contents, []byte("url(http://")) || bytes.Contains(contents, []byte("url(https://")) || bytes.Contains(contents, []byte("@import"))) {
			t.Errorf("external CSS load in %s", name)
		}
		return readErr
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompressedFrontendBudget(t *testing.T) {
	assets, err := Static()
	if err != nil {
		t.Fatal(err)
	}
	var total int
	err = fs.WalkDir(assets, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		ext := strings.ToLower(strings.TrimPrefix(filepathExtension(name), "."))
		if ext != "html" && ext != "css" && ext != "js" && ext != "mjs" {
			return nil
		}
		contents, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		var output bytes.Buffer
		writer, err := gzip.NewWriterLevel(&output, gzip.BestCompression)
		if err != nil {
			return err
		}
		if _, err := writer.Write(contents); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		total += output.Len()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const budget = 500 * 1024
	if total > budget {
		t.Fatalf("compressed frontend is %d bytes; budget is %d", total, budget)
	}
	t.Logf("compressed frontend: %d bytes", total)
}

func TestVendorManifestHashes(t *testing.T) {
	assets, err := Static()
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := fs.ReadFile(assets, "lib/vendor-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Packages []struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			License string `json:"license"`
			Files   []struct {
				Path   string `json:"path"`
				SHA256 string `json:"sha256"`
				Bytes  int    `json:"bytes"`
			} `json:"files"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Packages) != 3 {
		t.Fatalf("vendor package count=%d", len(manifest.Packages))
	}
	for _, dependency := range manifest.Packages {
		if dependency.Name == "" || dependency.Version == "" || dependency.License == "" {
			t.Fatalf("incomplete vendor metadata: %+v", dependency)
		}
		for _, file := range dependency.Files {
			contents, err := fs.ReadFile(assets, "lib/"+file.Path)
			if err != nil {
				t.Errorf("%s: %v", file.Path, err)
				continue
			}
			digest := sha256.Sum256(contents)
			if actual := hex.EncodeToString(digest[:]); actual != file.SHA256 {
				t.Errorf("%s SHA-256=%s, manifest=%s", file.Path, actual, file.SHA256)
			}
			if len(contents) != file.Bytes {
				t.Errorf("%s bytes=%d, manifest=%d", file.Path, len(contents), file.Bytes)
			}
		}
	}
}

func filepathExtension(name string) string {
	if index := strings.LastIndexByte(name, '.'); index >= 0 {
		return name[index:]
	}
	return ""
}
