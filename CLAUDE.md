# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## O projeto

`pagbank-extract` é um CLI em Go que baixa a movimentação de uma conta empresarial PagBank e a
grava em CSVs prontos para abrir no Excel em português. Duas fontes independentes, escolhidas pelo
subcomando: `edi` (extrato completo, JSON) e `transacoes` (só vendas, XML, API legada).

**Leia o [README.md](README.md) antes de mexer em qualquer fonte.** Ele documenta o que cada API
cobre, como obter cada credencial, o formato do CSV, os avisos que a aplicação emite e os dois
pontos que a documentação pública do PagBank não resolve. Este arquivo não repete nada disso.

## Comandos

```sh
task                 # = task build: compila em saida/pagbank-extract[.exe]
task test            # go test ./...
task test:update     # go test ./... -update — regrava os CSVs golden
task vet             # go vet ./...  (é o único "lint"; não há linter nem CI)
task run -- transacoes --from 2026-08-25 -v    # compila e executa
task --list          # tudo o que existe
```

Sem o [Task](https://taskfile.dev), o equivalente em Go direto: `go build ./cmd/pagbank-extract`,
`go test ./...`, `go vet ./...`.

Um teste só, ou um pacote só:

```sh
go test ./internal/source/legacy -run TestFetchPaginaEDeduplica -v
go test ./internal/source/edi -run 'TestFetch.*' -v
go test ./internal/sheet -update      # regrava só os golden desse pacote
```

`gofmt -l .` lista todos os arquivos numa cópia Windows: o `core.autocrlf` deixa os `.go` com CRLF
no disco e o gofmt quer LF. Não é desformatação — compare ignorando o `\r` antes de "arrumar" nada.

## Restrições que não se negociam

- **Só biblioteca padrão.** O `go.mod` não tem um único `require`, não há `go.sum`, e deve
  continuar assim — é o que permite compilar num clone novo sem rede. Antes de alcançar uma
  dependência, escreva o pedaço que falta: é o que `internal/httpx`, `internal/config` e o
  `latin1Reader` de `legacy.go` são.
- **Go 1.26.** `errors.AsType` (`internal/httpx/httpx.go:222`) e `math/rand/v2` não existem antes.
- **Credencial nunca entra por flag** — só pelo ambiente ou pelo `.env`, via `internal/config`. A
  linha de comando fica no histórico do shell e na lista de processos.
- **Todo segredo passa pelo `httpx.Redactor`** antes de chegar a log, erro ou saída. A API legada
  carrega o token na *query string*, então uma URL crua numa mensagem de erro vaza a credencial.
  Erro novo que leve URL ou corpo de resposta: redija. `Redactor.Error` não encadeia com `%w` de
  propósito — o erro original guarda o texto cru e um `%w` o traria de volta à tona.
- **Nenhum teste toca a rede.** Tudo é `httptest` mais fixture em `testdata/`, com o relógio fixado
  pelo campo `Now` do cliente. Em ponta a ponta, aponte `PAGBANK_EDI_BASE_URL` ou
  `PAGBANK_LEGACY_BASE_URL` para um servidor local.
- **Valor monetário nunca passa por `float`.** É transcrito como texto da API até o CSV:
  `json.Number.String()` no EDI, `decimalComma` em `internal/sheet/csv.go` na gravação.

## Idioma

Prosa, comentários, doc comments, mensagens ao usuário, textos de erro, nomes de teste e mensagens
de commit em **português**. Nos testes, `quero` é o "want" do idioma local — as asserções se leem
`t.Errorf("%s = %q, quero %q", ...)`.

Os identificadores Go seguem o costume de cada arquivo: a API genérica e o que espelha campo de API
ficam em inglês (`Table`, `Column`, `Kind`, `Source`, `Fetch`, `Result`, `MaxWindowDays`), os termos
do domínio e do fluxo em português (`SemDetalhes`, `tipos`, `meiosDePagamento`, `buscar`,
`detalhar`, `estadoDia`, `milhar`, `saida`). As flags misturam pelo mesmo critério: `--from`,
`--to`, `--out`, `--types`, mas `--sem-detalhes`.

Os doc comments explicam *por que* a solução é aquela, quase sempre citando o comportamento da API
que a forçou — `internal/sheet/sheet.go` e `internal/httpx/httpx.go` são a régua. Vale manter.

## Arquitetura

O fluxo é uma linha reta, sem estado global:

```
cmd/pagbank-extract  → flags, credenciais, escolha da fonte, relatório final
  internal/source    → contrato Source{Name, Fetch(ctx, Period)} → Result{Tables, Warnings}
    .../edi          → um GET por (tipo, dia, página); JSON de layout não publicado
    .../legacy       → janelas de 30 dias → páginas → 2ª passada de detalhe por transação
  internal/sheet     → Builder → Table → CSV
```

Invariantes que só aparecem lendo vários arquivos:

- **As fontes não compartilham schema.** Cada uma monta as próprias tabelas e nada dos campos de
  origem é descartado. Não tente unificar: o layout do EDI v3.00 não é documentado, e é por isso
  que `sheet.Builder` deixa uma coluna *surgir* na segunda página — `Builder.Column` a cria no ato
  e preenche com vazio as linhas anteriores, e rebaixa para `KindText` se o mesmo cabeçalho chegar
  com `Kind` conflitante.
- **A célula é canônica, não localizada.** O `Kind` diz o que ela guarda: `KindNumber` é
  `"-1234.56"`, `KindDate` é `"2006-01-02"`. Ponto e vírgula brasileiros, `dd/mm/aaaa`, BOM UTF-8 e
  CRLF entram só em `csv.go`, na gravação. Nunca formate para pt-BR dentro de uma fonte.
- **String parecida com número continua texto** (`normalizeString`, em `edi/json.go`). NSU, BIN e
  código de autorização são identificadores: tratá-los como número comeria o zero à esquerda.
- **Aviso ao usuário não é erro.** Dado parcial (`VALIDADO: FALSE`), dia sem movimento (404),
  detalhe que falhou, duplicata entre páginas, período cortado no presente: viram `Result.Warnf` e
  a extração continua. Só o que invalida a planilha inteira volta como `error`.
- **A gravação começa depois que a busca termina** (`extrair`, em `main.go`), para que um Ctrl+C no
  meio não deixe CSV pela metade.
- **O resumo é autoritativo sobre o detalhe.** `merge`, em `legacy.go`, só preenche campo vazio: um
  detalhe parcial nunca apaga o que a busca por data trouxe. E detalhe que falha não derruba a
  extração — a linha fica com o resumo e o total de falhas vira aviso.
- **Coluna de código cru é autoritativa.** As traduções de `status`, `type` e meio de pagamento são
  conveniência de leitura, gravadas *ao lado* do código, em coluna própria.

## Armadilhas da API legada (já resolvidas — não desfaça)

- `Accept: application/xml;charset=ISO-8859-1` é obrigatório nos **dois** endpoints. O RESTEasy
  antigo que serve essa API compara o media type com os parâmetros, e `application/xml` puro
  devolve 406. Pinado por `TestFetchEnviaAcceptComCharset` e
  `TestFetchDetalhesEnviaAcceptComCharset`.
- `finalDate` é cortado em *agora*, não no fim do dia, menos um minuto de `clockSkew`: o resto de
  hoje ainda é futuro e a API recusa a consulta inteira com o código `13009`.
- As datas vão no fuso de Brasília, fixo (`var brasilia`), porque a API não aceita deslocamento na
  query e o Brasil não tem horário de verão desde 2019.

## Códigos do PagBank (`internal/source/legacy/codigos.go`)

Os mapas ali só recebem código **confirmado numa conta real** — as tabelas que circulam em SDKs de
terceiros divergem entre si, e rotular uma linha errada é pior que deixá-la com `código N`. Ao
acrescentar um, registre no comentário de onde veio a confirmação, como os códigos `8` e `11` já
fazem. É a única mudança necessária: `describe` cuida do resto.

## Os CSVs golden

`internal/*/testdata/*.csv` são bytes exatos, com BOM e CRLF — e por isso o `.gitattributes` marca
esses caminhos com `-text`, para o git não normalizar as quebras de linha. CSV novo de teste vai sob
um `testdata/`, senão o `.gitignore` (que ignora `*.csv`) o engole.

**Nunca rode `-update` para fazer um golden que falha passar.** O `-update` regrava o arquivo a
partir do código sob teste, e o teste passa a comparar a saída consigo mesma. Leia o diff primeiro:
o golden é o que prova que o CSV continua abrindo no Excel com um duplo clique.

## Dois arquivos locais com dado real

Ambos ignorados pelo git, ambos fora dos testes — não leia, não cole em resposta, não versione:

- `.env` — as credenciais de verdade da conta.
- `saida/*.csv` — a saída de extrações reais, com códigos e valores de transações verdadeiras.

As fixtures e os golden vêm de dados sintéticos, e é assim que devem continuar.
