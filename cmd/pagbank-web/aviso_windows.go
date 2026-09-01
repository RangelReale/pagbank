//go:build windows

package main

import (
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

// Este binário é compilado com -H=windowsgui, então o Explorer não abre janela
// de console nenhuma — e o processo não tem para onde escrever. Um
// Fprintln(os.Stderr) aqui não falha: simplesmente não aparece a ninguém.
// A caixa de mensagem nativa é a única saída fora do navegador.
var (
	user32      = syscall.NewLazyDLL("user32.dll")
	messageBoxW = user32.NewProc("MessageBoxW")
)

const (
	mbOK            = 0x00000000
	mbIconWarning   = 0x00000030
	mbSetForeground = 0x00010000
)

// avisar mostra uma caixa de mensagem e espera o usuário fechá-la.
func avisar(titulo, texto string) {
	t, err := syscall.UTF16PtrFromString(texto)
	if err != nil {
		return
	}
	c, err := syscall.UTF16PtrFromString(titulo)
	if err != nil {
		return
	}
	messageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)),
		mbOK|mbIconWarning|mbSetForeground)
	// LazyProc.Call recebe uintptr, que o coletor não enxerga como ponteiro: sem
	// isto ele poderia recolher as duas strings enquanto a chamada corre.
	runtime.KeepAlive(t)
	runtime.KeepAlive(c)
}

// esconderJanela impede que o processo que abre o navegador pisque um console
// na tela — o programa inteiro existe para não mostrar nenhum.
func esconderJanela(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
