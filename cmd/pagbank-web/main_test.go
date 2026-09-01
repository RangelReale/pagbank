package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPastaDeTrabalhoPrefereOOverride(t *testing.T) {
	dir := t.TempDir()
	got, err := pastaDeTrabalho(dir)
	if err != nil {
		t.Fatalf("pastaDeTrabalho: %v", err)
	}
	if got != dir {
		t.Errorf("pastaDeTrabalho(%q) = %q, quero %q", dir, got, dir)
	}
}

func TestPastaDeTrabalhoNaoUsaODiretorioDeTrabalho(t *testing.T) {
	// O ponto do exercício: no Explorer o diretório de trabalho vem do atalho, e
	// "executar como administrador" o troca por C:\Windows\system32. A pasta que
	// vale é a do executável.
	t.Chdir(t.TempDir())

	got, err := pastaDeTrabalho("")
	if err != nil {
		t.Fatalf("pastaDeTrabalho: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if quero := filepath.Dir(exe); got != quero {
		cwd, _ := os.Getwd()
		t.Errorf("pastaDeTrabalho = %q, quero %q (o diretório de trabalho é %q)", got, quero, cwd)
	}
}
