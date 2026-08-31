// Package config resolve as credenciais e endereços da aplicação a partir do
// ambiente e de um arquivo .env opcional.
//
// Credenciais nunca entram por flag: a linha de comando fica no histórico do
// shell e aparece na lista de processos.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Nomes das variáveis lidas. Os *_BASE_URL existem para apontar os testes de
// ponta a ponta para um servidor local; em uso normal ficam vazios.
const (
	EnvEDIUser    = "PAGBANK_EDI_USER"
	EnvEDIToken   = "PAGBANK_EDI_TOKEN"
	EnvEDIBaseURL = "PAGBANK_EDI_BASE_URL"

	EnvEmail         = "PAGBANK_EMAIL"
	EnvToken         = "PAGBANK_TOKEN"
	EnvLegacyBaseURL = "PAGBANK_LEGACY_BASE_URL"
)

// Env é a união do ambiente do processo com o arquivo .env, nessa ordem de
// precedência.
type Env struct {
	file map[string]string
	Path string // caminho do .env lido, vazio se não havia nenhum
}

// Load lê o .env em path, se existir. A ausência do arquivo não é erro — quem
// exporta as variáveis no ambiente não precisa dele.
func Load(path string) (*Env, error) {
	e := &Env{file: map[string]string{}}

	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	e.Path = path
	sc := bufio.NewScanner(f)
	for line := 1; sc.Scan(); line++ {
		k, v, ok := parseLine(sc.Text())
		if !ok {
			continue
		}
		if k == "" {
			return nil, fmt.Errorf("%s:%d: linha inválida, esperava CHAVE=valor", path, line)
		}
		e.file[k] = v
	}
	return e, sc.Err()
}

// parseLine interpreta uma linha do .env. Devolve ok=false para linhas vazias e
// comentários, e k="" para linhas malformadas.
func parseLine(raw string) (k, v string, ok bool) {
	line := strings.TrimSpace(raw)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	name, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", true
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", true
	}
	value = strings.TrimSpace(value)
	// Aspas em volta do valor são delimitadores, não conteúdo.
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	return name, value, true
}

// Get devolve o valor da variável, dando prioridade ao ambiente do processo.
func (e *Env) Get(key string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return e.file[key]
}

// EDI são as credenciais da API do Extrato EDI.
type EDI struct {
	User    string // número do estabelecimento
	Token   string
	BaseURL string
}

// Legacy são as credenciais da API legada de transações.
type Legacy struct {
	Email   string
	Token   string
	BaseURL string
}

// Secrets lista os valores que não podem aparecer em log ou mensagem de erro.
func (c EDI) Secrets() []string    { return []string{c.Token} }
func (c Legacy) Secrets() []string { return []string{c.Token, c.Email} }

// EDI monta as credenciais do EDI, ou explica o que falta e como obter.
func (e *Env) EDI() (EDI, error) {
	c := EDI{
		User:    e.Get(EnvEDIUser),
		Token:   e.Get(EnvEDIToken),
		BaseURL: e.Get(EnvEDIBaseURL),
	}
	var faltando []string
	if c.User == "" {
		faltando = append(faltando, EnvEDIUser)
	}
	if c.Token == "" {
		faltando = append(faltando, EnvEDIToken)
	}
	if len(faltando) > 0 {
		return EDI{}, e.missing(faltando, "Credenciais do EDI")
	}
	return c, nil
}

// Legacy monta as credenciais da API legada, ou explica o que falta.
func (e *Env) Legacy() (Legacy, error) {
	c := Legacy{
		Email:   e.Get(EnvEmail),
		Token:   e.Get(EnvToken),
		BaseURL: e.Get(EnvLegacyBaseURL),
	}
	var faltando []string
	if c.Email == "" {
		faltando = append(faltando, EnvEmail)
	}
	if c.Token == "" {
		faltando = append(faltando, EnvToken)
	}
	if len(faltando) > 0 {
		return Legacy{}, e.missing(faltando, "Credenciais do painel")
	}
	return c, nil
}

// missing produz um erro que diz o que falta e onde aprender a conseguir.
func (e *Env) missing(faltando []string, secao string) error {
	arquivo := "um arquivo .env"
	if e.Path != "" {
		arquivo = e.Path
	}
	return fmt.Errorf("faltando %s %s: defina no ambiente ou em %s.\nComo obter: veja a seção %q do README.md",
		plural(len(faltando), "a variável", "as variáveis"),
		strings.Join(faltando, ", "), arquivo, secao)
}

func plural(n int, um, muitos string) string {
	if n == 1 {
		return um
	}
	return muitos
}
