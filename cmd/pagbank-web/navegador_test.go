package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArgumentosDeAppNaoMostramBarraDeEndereco(t *testing.T) {
	args := argumentosDeApp("http://127.0.0.1:5555/?t=abc", `C:\perfil`)

	quero := []string{
		"--app=http://127.0.0.1:5555/?t=abc",
		`--user-data-dir=C:\perfil`,
		"--no-first-run",
		"--no-default-browser-check",
	}
	for _, q := range quero {
		if !contem(args, q) {
			t.Errorf("args = %v, quero conter %q", args, q)
		}
	}
	// Sem --app a janela viria com barra de endereço, e o token da sessão
	// ficaria à mostra nela.
	if !strings.HasPrefix(args[0], "--app=") {
		t.Errorf("o primeiro argumento é %q, quero o --app", args[0])
	}
}

// navegadoresFalsos aponta as variáveis de ambiente para pastas temporárias e
// cria ali os executáveis pedidos, para exercitar a busca sem depender do que
// está instalado na máquina de quem roda o teste.
func navegadoresFalsos(t *testing.T, quais ...string) {
	t.Helper()
	raiz := t.TempDir()
	for _, v := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		t.Setenv(v, raiz)
	}
	for _, q := range quais {
		caminho := filepath.Join(raiz, q)
		if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(caminho, []byte("nao sou um navegador"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

const (
	edgeFalso   = `Microsoft\Edge\Application\msedge.exe`
	chromeFalso = `Google\Chrome\Application\chrome.exe`
)

func TestNavegadorDeAppPrefereOEdge(t *testing.T) {
	soNoWindows(t)
	navegadoresFalsos(t, edgeFalso, chromeFalso)

	got := navegadorDeApp()
	if !strings.HasSuffix(got, "msedge.exe") {
		t.Errorf("navegadorDeApp = %q, quero o Edge — ele vem no Windows e o Chrome não", got)
	}
}

func TestNavegadorDeAppCaiParaOChrome(t *testing.T) {
	soNoWindows(t)
	navegadoresFalsos(t, chromeFalso)

	got := navegadorDeApp()
	if !strings.HasSuffix(got, "chrome.exe") {
		t.Errorf("navegadorDeApp = %q, quero o Chrome", got)
	}
}

func TestSemChromiumNaoHaModoApp(t *testing.T) {
	soNoWindows(t)
	navegadoresFalsos(t) // pastas vazias

	if got := navegadorDeApp(); got != "" {
		t.Errorf("navegadorDeApp = %q, quero vazio para cair no navegador padrão", got)
	}
}

func TestPerfilFicaForaDaPastaDoExecutavel(t *testing.T) {
	perfil := perfilDaJanela()
	if perfil == "" {
		t.Fatal("perfilDaJanela devolveu vazio")
	}

	// São dezenas de MB de arquivos do Chromium, e a pasta do executável é onde
	// o usuário procura as planilhas.
	pasta, err := pastaDeTrabalho("")
	if err != nil {
		t.Fatal(err)
	}
	if dentro(perfil, pasta) {
		t.Errorf("o perfil %q está dentro da pasta de trabalho %q", perfil, pasta)
	}
}

func soNoWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("a busca por Chromium só vale no Windows")
	}
}

func contem(lista []string, v string) bool {
	for _, item := range lista {
		if item == v {
			return true
		}
	}
	return false
}

func dentro(caminho, pasta string) bool {
	rel, err := filepath.Rel(pasta, caminho)
	return err == nil && !strings.HasPrefix(rel, "..")
}
