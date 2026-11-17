package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestIndexHTML_UpdateDialogMarkers(t *testing.T) {
	for _, marker := range []string{`role="dialog"`, `aria-modal="true"`, `role="tablist"`, `>Binary</button>`, `>Docker run</button>`, `>Docker Compose</button>`, `docker compose pull && docker compose up -d`} {
		if !strings.Contains(indexHTML, marker) {
			t.Errorf("missing %s", marker)
		}
	}
	code := sliceBetween(t, indexHTML, "function updateShouldShow(", "/* ---- theme set ---- */")
	if strings.Contains(code, "innerHTML") {
		t.Error("API update logic must not assign innerHTML")
	}
}

func TestIndexHTML_UpdateDialogPhoneWrap(t *testing.T) {
	phone := sliceBetween(t, indexHTML, "@media(max-width:600px){", "\n}")
	if !strings.Contains(phone, ".update-dialog pre{white-space:pre-wrap;overflow-wrap:anywhere;overflow-x:visible}") {
		t.Error("phone media block must wrap update commands without horizontal scrolling")
	}
}

func TestIndexHTML_UpdateDialogSelectedTab(t *testing.T) {
	if !strings.Contains(indexHTML, ".update-tabs [aria-selected=true]{border:2px solid var(--accent);color:var(--head);font-weight:700}") {
		t.Error("selected update tab must have a prominent accent border and bold text")
	}
	if !strings.Contains(indexHTML, ".update-tabs [role=tab]:focus-visible{outline:2px solid var(--head);outline-offset:2px}") {
		t.Error("keyboard focus on update tabs must remain visible")
	}
}

func TestIndexHTML_UpdateDialogTab(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := funcBody(t, indexHTML, "function updateDialogTab(")
	runNode(t, node, code+`for(const [value,want] of [['docker','docker'],['binary','binary'],['systemd','binary'],['other','binary'],[undefined,'binary'],[null,'binary']]) if(updateDialogTab(value)!==want) throw Error(String(value));`)
}

func TestIndexHTML_UpdateDialogCommands(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := sliceBetween(t, indexHTML, "const UPDATE_COMMANDS=", "function updateDialogTab(")
	runNode(t, node, code+"\n"+`const expected={binary:'urwebdash update',docker:'docker pull ghcr.io/full-bars/urwebdash:latest\ndocker rm -f urwebdash\ndocker run -d --name urwebdash \\\n  -p 127.0.0.1:3001:3001 \\\n  -v urwebdash-data:/data \\\n  ghcr.io/full-bars/urwebdash',compose:'docker compose pull && docker compose up -d'}; for(const k of Object.keys(expected)) if(UPDATE_COMMANDS[k]!==expected[k]) throw Error(k+': '+JSON.stringify(UPDATE_COMMANDS[k]));`)
}

func TestIndexHTML_UpdateCopyFallback(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	code := funcBody(t, indexHTML, "function updateCopyMethod(")
	runNode(t, node, code+`for(const [available,want] of [[true,'clipboard'],[false,'fallback'],[null,'fallback'],[undefined,'fallback']]) if(updateCopyMethod(available)!==want) throw Error(String(available));`)
}
