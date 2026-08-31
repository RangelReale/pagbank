package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func escreverEnv(t *testing.T, conteudo string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(conteudo), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadArquivoAusenteNaoEErro(t *testing.T) {
	e, err := Load(filepath.Join(t.TempDir(), "nao-existe"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if e.Path != "" {
		t.Errorf("Path = %q, quero vazio", e.Path)
	}
	if got := e.Get(EnvEDIUser); got != "" {
		t.Errorf("Get = %q", got)
	}
}

func TestLoadInterpretaOArquivo(t *testing.T) {
	path := escreverEnv(t, strings.Join([]string{
		"# comentário",
		"",
		"PAGBANK_EDI_USER=123456",
		`export PAGBANK_EDI_TOKEN="tok-com-espaço "`,
		"  PAGBANK_EMAIL = conta@empresa.com.br  ",
		"PAGBANK_TOKEN='aspas-simples'",
		"VAZIA=",
	}, "\n"))

	e, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	casos := map[string]string{
		EnvEDIUser:  "123456",
		EnvEDIToken: "tok-com-espaço ",
		EnvEmail:    "conta@empresa.com.br",
		EnvToken:    "aspas-simples",
		"VAZIA":     "",
	}
	for k, want := range casos {
		if got := e.Get(k); got != want {
			t.Errorf("Get(%q) = %q, quero %q", k, got, want)
		}
	}
	if e.Path != path {
		t.Errorf("Path = %q, quero %q", e.Path, path)
	}
}

func TestLoadRejeitaLinhaSemIgual(t *testing.T) {
	_, err := Load(escreverEnv(t, "PAGBANK_TOKEN"))
	if err == nil || !strings.Contains(err.Error(), "CHAVE=valor") {
		t.Fatalf("erro = %v, quero explicação do formato", err)
	}
}

func TestAmbienteTemPrecedenciaSobreOArquivo(t *testing.T) {
	e, err := Load(escreverEnv(t, EnvEDIToken+"=do-arquivo"))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Get(EnvEDIToken); got != "do-arquivo" {
		t.Fatalf("Get = %q, quero do-arquivo", got)
	}
	t.Setenv(EnvEDIToken, "do-ambiente")
	if got := e.Get(EnvEDIToken); got != "do-ambiente" {
		t.Errorf("Get = %q, quero do-ambiente", got)
	}
}

func TestVariavelDeAmbienteVaziaNaoApagaOArquivo(t *testing.T) {
	e, err := Load(escreverEnv(t, EnvEDIToken+"=do-arquivo"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvEDIToken, "")
	if got := e.Get(EnvEDIToken); got != "do-arquivo" {
		t.Errorf("Get = %q, quero do-arquivo", got)
	}
}

func TestEDIExigeAsDuasCredenciais(t *testing.T) {
	path := escreverEnv(t, EnvEDIUser+"=123456")
	e, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvEDIToken, "")

	_, err = e.EDI()
	if err == nil {
		t.Fatal("esperava erro")
	}
	msg := err.Error()
	for _, want := range []string{EnvEDIToken, path, "Credenciais do EDI", "README.md"} {
		if !strings.Contains(msg, want) {
			t.Errorf("erro não menciona %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, EnvEDIUser) {
		t.Errorf("erro cita variável que estava presente: %s", msg)
	}
}

func TestEDIELegacyCompletos(t *testing.T) {
	e, err := Load(escreverEnv(t, strings.Join([]string{
		EnvEDIUser + "=123456",
		EnvEDIToken + "=tok-edi",
		EnvEmail + "=conta@empresa.com.br",
		EnvToken + "=tok-legado",
		EnvEDIBaseURL + "=http://127.0.0.1:1/edi",
	}, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{EnvEDIUser, EnvEDIToken, EnvEmail, EnvToken, EnvEDIBaseURL, EnvLegacyBaseURL} {
		t.Setenv(k, "")
	}

	edi, err := e.EDI()
	if err != nil {
		t.Fatalf("EDI: %v", err)
	}
	if edi.User != "123456" || edi.Token != "tok-edi" || edi.BaseURL != "http://127.0.0.1:1/edi" {
		t.Errorf("EDI = %+v", edi)
	}
	if got := edi.Secrets(); len(got) != 1 || got[0] != "tok-edi" {
		t.Errorf("Secrets = %v", got)
	}

	leg, err := e.Legacy()
	if err != nil {
		t.Fatalf("Legacy: %v", err)
	}
	if leg.Email != "conta@empresa.com.br" || leg.Token != "tok-legado" {
		t.Errorf("Legacy = %+v", leg)
	}
	// O e-mail também é segredo: ele viaja na query string junto com o token.
	if got := leg.Secrets(); len(got) != 2 {
		t.Errorf("Secrets = %v, quero token e e-mail", got)
	}
}

func TestMensagemDeErroSemArquivoEnv(t *testing.T) {
	e, err := Load(filepath.Join(t.TempDir(), "nao-existe"))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{EnvEmail, EnvToken} {
		t.Setenv(k, "")
	}
	_, err = e.Legacy()
	if err == nil || !strings.Contains(err.Error(), "um arquivo .env") {
		t.Fatalf("erro = %v", err)
	}
	if !strings.Contains(err.Error(), "as variáveis") {
		t.Errorf("erro deveria usar o plural: %v", err)
	}
}
