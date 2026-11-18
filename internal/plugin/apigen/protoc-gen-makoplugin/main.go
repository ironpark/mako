// protoc-gen-makoplugin is the protoc plugin buf runs (`make proto`) to
// generate the plugin Host API from proto/mako/plugin/v1; see
// internal/plugin/apigen.
package main

import (
	"fmt"
	"io"
	"os"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/ironpark/mako/internal/plugin/apigen"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "protoc-gen-makoplugin:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	data, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	req := &pluginpb.CodeGeneratorRequest{}
	if err := proto.Unmarshal(data, req); err != nil {
		return err
	}
	resp := &pluginpb.CodeGeneratorResponse{
		SupportedFeatures: proto.Uint64(uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)),
	}
	// The files to generate for are the module's; the API is those of its
	// package, and the rest is passed over.
	generate := map[string]bool{}
	for _, name := range req.GetFileToGenerate() {
		generate[name] = true
	}
	var files = req.GetProtoFile()
	mine := false
	for _, f := range files {
		if generate[f.GetName()] && f.GetPackage() == apigen.Package {
			mine = true
		}
	}
	if mine {
		gen, err := apigen.Generate(files)
		if err != nil {
			resp.Error = proto.String(err.Error())
		}
		for _, f := range gen {
			resp.File = append(resp.File, &pluginpb.CodeGeneratorResponse_File{Name: proto.String(f.Name), Content: proto.String(string(f.Content))})
		}
	}
	b, err := proto.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = out.Write(b)
	return err
}
