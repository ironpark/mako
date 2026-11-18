package apigen_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/ironpark/mako/internal/plugin/apigen"
	_ "github.com/ironpark/mako/internal/plugin/hostapi" // registers the schema's Go
)

// root is the module's root, from this package's folder.
const root = "../../.."

// schema is the Host API's schema as `make proto` kept it, comments and
// all.
func schema(t *testing.T) *descriptorpb.FileDescriptorSet {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, apigen.OutDir, "hostapi.binpb"))
	if err != nil {
		t.Fatal(err)
	}
	set := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(data, set); err != nil {
		t.Fatal(err)
	}
	return set
}

// TestGeneratedFilesMatchSchema generates the Host API again from the
// schema kept, and fails on any file that differs from what is committed:
// the generated files were edited, or the generator changed without
// `make proto`.
func TestGeneratedFilesMatchSchema(t *testing.T) {
	files, err := apigen.Generate(schema(t).GetFile())
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		have, err := os.ReadFile(filepath.Join(root, f.Name))
		if err != nil {
			t.Errorf("%s: %v; run make proto", f.Name, err)
			continue
		}
		if !bytes.Equal(have, f.Content) {
			t.Errorf("%s differs from what the schema generates; run make proto", f.Name)
		}
	}
}

// TestSchemaKeptMatchesGo checks the schema kept against the one the
// generated Go messages carry, so neither was made of a schema the other
// was not: both come of one run of make proto.
func TestSchemaKeptMatchesGo(t *testing.T) {
	set := schema(t)
	if len(set.GetFile()) == 0 {
		t.Fatal("no files kept")
	}
	for _, kept := range set.GetFile() {
		fd, err := protoregistry.GlobalFiles.FindFileByPath(kept.GetName())
		if err != nil {
			t.Fatalf("%s: %v", kept.GetName(), err)
		}
		embedded := protodesc.ToFileDescriptorProto(fd)
		stripped := proto.CloneOf(kept)
		stripped.SourceCodeInfo = nil
		if !proto.Equal(embedded, stripped) {
			t.Errorf("%s: the schema kept is not the one the Go was generated from; run make proto", kept.GetName())
		}
	}
}
