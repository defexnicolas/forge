package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newToolNewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "new <runtime> <name>",
		Short: "Generate a new tool template (python, node, process)",
		Long:  "Generates a tool scaffold in .forge/tools/<name>/ with tool.toml and a starter script.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runtime := args[0]
			name := args[1]
			cwd, _ := os.Getwd()
			dir := filepath.Join(cwd, ".forge", "tools", name)
			if _, err := os.Stat(dir); err == nil {
				return fmt.Errorf("tool already exists: %s", dir)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}

			switch runtime {
			case "python":
				return generatePythonTool(dir, name)
			case "node":
				return generateNodeTool(dir, name)
			case "process", "generic":
				return generateProcessTool(dir, name)
			default:
				return fmt.Errorf("unknown runtime: %s (use python, node, or process)", runtime)
			}
		},
	}
}

func generatePythonTool(dir, name string) error {
	toml := fmt.Sprintf(`name = "%s"
description = "Custom Python tool"
runtime = "process"
command = "python ./main.py"
permission = "ask"

[schema]
type = "object"
required = ["input"]

[schema.properties.input]
type = "string"
description = "The input for the tool"
`, name)

	script := `#!/usr/bin/env python3
"""Tool: ` + name + `"""
import json
import sys

request = json.load(sys.stdin)
user_input = request.get("input", {}).get("input", "")

# Your tool logic here.
result = f"Processed: {user_input}"

json.dump({
    "title": "` + name + `",
    "summary": result,
    "content": [
        {"type": "text", "text": result}
    ],
    "changedFiles": []
}, sys.stdout)
`

	if err := os.WriteFile(filepath.Join(dir, "tool.toml"), []byte(toml), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(script), 0o644); err != nil {
		return err
	}
	fmt.Printf("Created Python tool: %s\n", dir)
	fmt.Println("  tool.toml  - tool configuration")
	fmt.Println("  main.py    - tool implementation")
	return nil
}

func generateNodeTool(dir, name string) error {
	toml := fmt.Sprintf(`name = "%s"
description = "Custom Node.js tool"
runtime = "process"
command = "node ./index.js"
permission = "ask"

[schema]
type = "object"
required = ["input"]

[schema.properties.input]
type = "string"
description = "The input for the tool"
`, name)

	script := `#!/usr/bin/env node
// Tool: ` + name + `
const chunks = [];
process.stdin.on('data', (chunk) => chunks.push(chunk));
process.stdin.on('end', () => {
  const request = JSON.parse(Buffer.concat(chunks).toString());
  const input = request.input?.input || '';

  // Your tool logic here.
  const result = ` + "`Processed: ${input}`" + `;

  process.stdout.write(JSON.stringify({
    title: '` + name + `',
    summary: result,
    content: [{ type: 'text', text: result }],
    changedFiles: []
  }));
});
`

	if err := os.WriteFile(filepath.Join(dir, "tool.toml"), []byte(toml), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "index.js"), []byte(script), 0o644); err != nil {
		return err
	}
	fmt.Printf("Created Node.js tool: %s\n", dir)
	fmt.Println("  tool.toml  - tool configuration")
	fmt.Println("  index.js   - tool implementation")
	return nil
}

func generateProcessTool(dir, name string) error {
	toml := fmt.Sprintf(`name = "%s"
description = "Custom process tool"
runtime = "process"
command = "./run.sh"
permission = "ask"

[schema]
type = "object"
required = ["input"]

[schema.properties.input]
type = "string"
description = "The input for the tool"
`, name)

	script := `#!/bin/bash
# Tool: ` + name + `
# Reads JSON from stdin, writes JSON result to stdout.

INPUT=$(cat)
QUERY=$(echo "$INPUT" | python3 -c "import json,sys; print(json.load(sys.stdin).get('input',{}).get('input',''))" 2>/dev/null || echo "")

# Your tool logic here.
RESULT="Processed: $QUERY"

cat <<EOF
{
  "title": "` + name + `",
  "summary": "$RESULT",
  "content": [{"type": "text", "text": "$RESULT"}],
  "changedFiles": []
}
EOF
`

	if err := os.WriteFile(filepath.Join(dir, "tool.toml"), []byte(toml), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		return err
	}
	fmt.Printf("Created process tool: %s\n", dir)
	fmt.Println("  tool.toml  - tool configuration")
	fmt.Println("  run.sh     - tool implementation")
	return nil
}
