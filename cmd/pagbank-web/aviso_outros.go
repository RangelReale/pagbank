//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
)

// Fora do Windows não há -H=windowsgui nem caixa de mensagem nativa, e o
// terminal continua sendo a saída natural. Estas duas existem para o
// `go build ./...` e os testes continuarem passando em Linux e macOS.

// avisar escreve a mensagem no terminal.
func avisar(titulo, texto string) {
	fmt.Fprintf(os.Stderr, "\n%s\n%s\n\n", titulo, texto)
}

// esconderJanela não tem o que fazer aqui.
func esconderJanela(*exec.Cmd) {}
