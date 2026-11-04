package upgrader

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// Rewrite updates the first component version in the relevant source attribute
// of a Terragrunt configuration or lambdas module file. Only the matching
// version bytes are changed, preserving HCL formatting, comments, and template
// interpolation syntax exactly.
func Rewrite(content []byte, filename, component, targetVersion string) ([]byte, bool, error) {
	filename = filepath.Base(filename)
	if filename != "terragrunt.hcl" && filename != "lambdas.tf" {
		return content, false, nil
	}

	file, diagnostics := hclwrite.ParseConfig(content, filename, hcl.Pos{Line: 1, Column: 1})
	if diagnostics.HasErrors() {
		return content, false, fmt.Errorf("parse %s: %s", filename, diagnostics.Error())
	}

	var source *hclwrite.Attribute
	switch filename {
	case "terragrunt.hcl":
		if block := file.Body().FirstMatchingBlock("terraform", nil); block != nil {
			source = block.Body().GetAttribute("source")
		}
	case "lambdas.tf":
		if block := file.Body().FirstMatchingBlock("module", []string{"lambdas"}); block != nil {
			source = block.Body().GetAttribute("source")
		}
	}
	if source == nil {
		return content, false, nil
	}

	versionPattern, err := regexp.Compile("(" + component + `-)(\d+\.\d+\.\d+)`)
	if err != nil {
		return content, false, fmt.Errorf("compile component version pattern: %w", err)
	}
	for _, token := range source.Expr().BuildTokens(nil) {
		if token.Type != hclsyntax.TokenQuotedLit && token.Type != hclsyntax.TokenStringLit {
			continue
		}
		match := versionPattern.FindSubmatchIndex(token.Bytes)
		if match == nil {
			continue
		}
		versionStart, versionEnd := match[3], match[1]
		if bytes.Equal(token.Bytes[versionStart:versionEnd], []byte(targetVersion)) {
			return content, false, nil
		}

		offset := 0
		for _, fileToken := range file.BuildTokens(nil) {
			offset += fileToken.SpacesBefore
			if fileToken == token {
				start := offset + versionStart
				end := offset + versionEnd
				updated := make([]byte, 0, len(content)-(end-start)+len(targetVersion))
				updated = append(updated, content[:start]...)
				updated = append(updated, targetVersion...)
				updated = append(updated, content[end:]...)
				return updated, true, nil
			}
			offset += len(fileToken.Bytes)
		}
		return content, false, fmt.Errorf("locate source token in %s", filename)
	}
	return content, false, nil
}
