package web_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/neverbot/maestro/internal/assert"
)

// **Every module this repository ships must parse as a module.**
func TestEveryModuleParsesAsAModule(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed; this guard needs the runtime the browser is closest to")
	}
	modules := ownModules(t)
	assert.Must(t, len(modules) != 0, "no module was examined, so this guard holds nothing")
	for _, module := range modules {
		source, err := os.ReadFile(module)
		assert.Must(t, err == nil, "read %s: %v", module, err)
		cmd := exec.Command("node", "--input-type=module", "--check")
		cmd.Stdin = strings.NewReader(string(source))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse as a module:\n%s", module, out)
		}
	}
}
