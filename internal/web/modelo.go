package web

import (
	"os"
	"path/filepath"
	"strings"
)

// NomeConfig é o arquivo de credenciais que a interface web procura ao lado do
// executável. Não é ".env" como na linha de comando: no Explorer, com as
// extensões ocultas, um arquivo só com extensão fica invisível ou impossível de
// criar, e o usuário final não tem por onde começar.
const NomeConfig = "config.env"

// modelo é o config.env que nasce na primeira execução.
//
// Sem acentos e com CRLF de propósito: o Bloco de Notas das versões mais antigas
// do Windows lê UTF-8 sem BOM como ANSI, e é o programa que abre um .env com um
// duplo clique numa máquina recém-instalada.
const modelo = "# Credenciais do PagBank. Preencha as duas linhas abaixo e salve.\r\n" +
	"#\r\n" +
	"# O token NAO e a senha da conta. Ele e gerado no painel do vendedor, em\r\n" +
	"# Preferencias > Integracoes > Token de seguranca.\r\n" +
	"#\r\n" +
	"# Guarde este arquivo: quem o tiver consegue consultar as vendas da conta.\r\n" +
	"\r\n" +
	"PAGBANK_EMAIL=\r\n" +
	"PAGBANK_TOKEN=\r\n"

// CaminhoConfig é o config.env da pasta dir.
func CaminhoConfig(dir string) string { return filepath.Join(dir, NomeConfig) }

// GarantirConfigModelo grava um config.env em branco em dir, se ainda não houver
// um, e diz se acabou de criá-lo.
//
// A abertura é O_EXCL: um arquivo já preenchido sobrevive a qualquer execução
// seguinte, mesmo que este código mude. Encontrar o arquivo não é erro — é o
// caso comum a partir da segunda vez que o programa roda.
func GarantirConfigModelo(dir string) (criado bool, err error) {
	f, err := os.OpenFile(CaminhoConfig(dir), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()

	if _, err := f.WriteString(modelo); err != nil {
		return false, err
	}
	return true, f.Close()
}

// PastaGravavel confirma que dá para escrever em dir antes de o programa
// prometer qualquer coisa ao usuário.
//
// O executável pode ter sido copiado para Arquivos de Programas, para um
// compartilhamento somente leitura ou para uma pasta em quarentena do Downloads,
// e aí nem o config.env nem o CSV poderiam ser gravados. Descobrir isso agora é
// muito melhor do que depois de gastar minutos numa extração.
func PastaGravavel(dir string) error {
	f, err := os.CreateTemp(dir, ".pagbank-*")
	if err != nil {
		return err
	}
	nome := f.Name()
	f.Close()
	return os.Remove(nome)
}

// ComoObterOToken é o texto que a página mostra a quem ainda não tem credencial.
// É o mesmo caminho descrito no README, escrito para quem nunca abriu o painel.
var ComoObterOToken = []string{
	"Entre no painel do vendedor do PagBank com o e-mail e a senha da conta.",
	"Vá em Preferências, depois Integrações, depois Token de segurança.",
	"Gere o token e copie o código que aparecer.",
	"O token não é a senha da conta: é um código só para integrações.",
}

// linhasDoModelo devolve o modelo em linhas, para a página mostrá-lo.
func linhasDoModelo() []string {
	return strings.Split(strings.ReplaceAll(strings.TrimRight(modelo, "\r\n"), "\r\n", "\n"), "\n")
}
