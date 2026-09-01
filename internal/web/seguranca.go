package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// NovoToken sorteia o token de sessão desta execução.
//
// Ele morre com o processo e viaja na query string, não em cookie: cookie de
// "http://127.0.0.1" ignora a porta, então qualquer outro programa local
// servindo em outra porta leria e sobrescreveria o nosso.
func NovoToken() string {
	b := make([]byte, 16)
	// rand.Read do crypto/rand nunca falha desde o Go 1.24: ou devolve bytes ou
	// derruba o processo.
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// autorizado decide se a requisição pode ser atendida.
//
// Um servidor em 127.0.0.1 é alcançável por qualquer página aberta no navegador
// do usuário, e esta aqui dispara consultas autenticadas ao PagBank e grava
// arquivos em disco. São três guardas, do mais para o menos importante:
//
//	token          barra a varredura de portas em localhost feita por uma página
//	               qualquer, que não tem como adivinhar 16 bytes sorteados
//	Host           barra o DNS rebinding, em que um domínio hostil passa a
//	               resolver para 127.0.0.1 e fala com este servidor como se
//	               fosse mesma origem — nesse caso o Host chega com o domínio
//	Sec-Fetch-Site barra o que o navegador já sabe ser requisição de outra
//	               origem; ausente é aceito, porque o token já cobre o resto
//
// O que isto não defende: outro programa rodando na mesma conta de usuário. Ele
// lê o config.env e o CSV direto do disco, então a porta HTTP não acrescenta
// exposição nenhuma.
func (s *Servidor) autorizado(r *http.Request) bool {
	if !hostLocal(r.Host) {
		return false
	}
	if v := r.Header.Get("Sec-Fetch-Site"); v != "" && v != "same-origin" && v != "none" {
		return false
	}
	dado := r.URL.Query().Get("t")
	return subtle.ConstantTimeCompare([]byte(dado), []byte(s.o.Token)) == 1
}

// hostLocal aceita só os nomes pelos quais o próprio navegador chega aqui.
func hostLocal(host string) bool {
	nome := host
	if i := strings.LastIndex(host, ":"); i >= 0 && !strings.HasSuffix(host, "]") {
		nome = host[:i]
	}
	switch strings.Trim(nome, "[]") {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// cabecalhosDeSeguranca vale para toda resposta.
//
// A política de conteúdo é fechada em 'none' e reaberta só no que a página usa:
// ela não carrega recurso externo nenhum — sem CDN, sem fonte remota —, tanto
// para funcionar numa máquina sem internet liberada quanto para não contar a
// terceiro nenhum que alguém está olhando a movimentação da conta.
//
// O no-referrer importa porque o token está na URL: sem ele, um clique num link
// externo o levaria junto no cabeçalho Referer.
func cabecalhosDeSeguranca(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action 'none'; base-uri 'none'")
}
