# pagbank-extract

Extrai a movimentação de uma conta empresarial PagBank para planilhas CSV, prontas para abrir
no Excel em português.

Escrito em Go, só com a biblioteca padrão — não há dependências para instalar.

```
go build ./cmd/pagbank-extract
```

## Qual fonte usar

O PagBank não tem uma API única de "extrato da conta". Existem duas fontes viáveis, com
credenciais, cobertura e limites bem diferentes:

| | `edi` | `transacoes` |
|---|---|---|
| API | [Extrato EDI v3.00](https://developer.pagbank.com.br/docs/api-do-extrato-edi) | [transações v2 (legada)](https://developer.pagbank.com.br/v1/reference/api-checkout-transparente-consulta-transacoes-por-data-ou-codigo-de-referencia) |
| Cobre | transações, movimentação financeira, saques e saldos | só vendas |
| Histórico | desde a ativação do EDI | últimos 6 meses |
| Atualidade | D-1 (o dia fecha no dia seguinte) | tempo real |
| Credencial | chamado no Pipefy, chega por e-mail | gerada na hora, no painel |
| Formato | JSON | XML |

**Para conferência contábil e conciliação, use `edi`** — é a única fonte que traz a liquidação
dos recebíveis, os saques e os saldos. O `transacoes` serve como acesso imediato enquanto o EDI
não é liberado, ou para conferir vendas do dia corrente, que no EDI ainda não fecharam.

## Credenciais

Ficam no ambiente ou em um arquivo `.env` no diretório de trabalho — **nunca em flag**, que
apareceria no histórico do shell e na lista de processos. Copie `.env.example` para `.env` e
preencha o que for usar; o `.gitignore` já ignora o `.env`.

### Credenciais do EDI

Para `pagbank-extract edi`, duas variáveis:

```
PAGBANK_EDI_USER=<número do estabelecimento>
PAGBANK_EDI_TOKEN=<token da API EDI>
```

Como obter, segundo a [documentação do EDI](https://developer.pagbank.com.br/docs/edi):

1. A conta precisa ser de vendedor e estar ativa.
2. Abra um chamado no [portal de ativação (Pipefy)](https://app.pipefy.com/organizations/142456/interfaces/3668b8ed-d930-4bcf-8038-8a00d3ed6901)
   escolhendo **"Novas Ativações - EDI"**.
3. No chamado, escolha o modelo de token:
   - **1xN** — um único token para vários estabelecimentos;
   - **1x1** — um token por estabelecimento.
4. O token chega no e-mail cadastrado. O `PAGBANK_EDI_USER` é o número do estabelecimento.

Não existe sandbox do EDI. Dúvidas de ativação: `edi@pagbank.com.br`.

### Credenciais do painel

Para `pagbank-extract transacoes`, duas variáveis:

```
PAGBANK_EMAIL=<e-mail de acesso à conta>
PAGBANK_TOKEN=<token da API, gerado no painel>
```

O token é gerado no painel do vendedor, em Preferências → Integrações → Token de segurança.
**Não é a senha da conta.**

## Uso

```sh
# Extrato completo de agosto, um CSV por tipo de movimento
pagbank-extract edi --from 2026-08-01 --to 2026-08-31

# Só a movimentação financeira e os saldos, em outro diretório
pagbank-extract edi --from 2026-08-01 --types financial,balances --out ./agosto

# Vendas dos últimos dias pela API legada, com progresso
pagbank-extract transacoes --from 2026-08-25 -v
```

`--to` é opcional e vale hoje. `--out` é opcional e vale `saida/`.

Os arquivos gerados:

| Comando | Arquivos |
|---|---|
| `edi` | `transactional.csv`, `financial.csv`, `cashouts.csv`, `balances.csv` |
| `transacoes` | `transacoes.csv` |

Rode `pagbank-extract help`, ou `pagbank-extract edi -h`, para todas as flags.

## O CSV

Formatado para o Excel em português: separador `;`, vírgula decimal, datas `dd/mm/aaaa`,
BOM UTF-8 (sem ele o Excel corrompe os acentos) e fim de linha `\r\n`. Basta abrir com um
duplo clique — não precisa passar pelo assistente de importação.

Valores monetários são transcritos **exatamente** como a API os devolve, sem passar por
ponto flutuante, para não introduzir erro de arredondamento em centavos.

## Duas coisas para conferir com dados reais

O código foi escrito a partir da documentação pública, que é incompleta em dois pontos. Ambos
foram tratados de forma a não inventar dado nenhum, mas valem uma conferência na primeira
extração de verdade:

**1. Escala dos valores do EDI.** O layout do JSON do EDI v3.00 não é publicado, então a
aplicação transcreve cada campo como veio. Layouts EDI costumam expressar dinheiro em centavos
inteiros. Se na primeira extração os valores vierem 100× maiores que o esperado, é isso —
avise e a conversão entra como uma lista de campos a dividir, em vez de um palpite aplicado a
tudo.

**2. Tabela de `<type>` das transações.** No comando `transacoes`, os códigos de `status` e de
meio de pagamento ganham uma coluna de descrição ao lado da coluna com o código cru. Já a
tabela do campo `type` não consta mais da documentação pública e as listas que circulam em SDKs
de terceiros divergem entre si, então esse campo aparece como `código N` em vez de arriscar
rotular a linha errada. Confirmados os valores numa conta real, preencha o mapa `tipos` em
[`internal/source/legacy/codigos.go`](internal/source/legacy/codigos.go) — é a única mudança
necessária.

Em ambos os casos **a coluna com o valor cru é a autoritativa**: nada é descartado.

## Avisos que a aplicação emite

- `VALIDADO=FALSE` — o PagBank ainda estava processando aquele dia. Os dados de um dia só são
  garantidos em D+1; reextraia no dia seguinte para conferir.
- `sem extrato disponível (HTTP 404)` — normalmente é dia sem movimento, mas é também o que a
  API responde para data anterior à ativação do EDI.
- `transações repetidas descartadas` — a paginação da API legada corre sobre dados vivos, e a
  mesma transação pode reaparecer em outra página. A deduplicação é pelo código da transação.
- `o período termina no futuro` — a API legada não responde por datas futuras, e o resto do dia
  de hoje é futuro: a consulta é cortada no instante atual. Rodar de novo mais tarde traz as
  vendas que faltavam.

## Desenvolvimento

```sh
go test ./...                      # tudo offline: httptest e fixtures, nenhum teste toca a rede
go test ./... -update              # regrava os CSVs golden em testdata
go vet ./...
```

Estrutura:

| Pacote | Papel |
|---|---|
| `cmd/pagbank-extract` | linha de comando |
| `internal/sheet` | modelo de tabela e escrita do CSV |
| `internal/source/edi` | cliente do Extrato EDI e achatamento do JSON |
| `internal/source/legacy` | cliente da API legada de transações |
| `internal/httpx` | retry, espaçamento de chamadas e redação de segredos |
| `internal/config` | credenciais vindas do ambiente e do `.env` |

Para apontar os testes de ponta a ponta para um servidor local, defina
`PAGBANK_EDI_BASE_URL` ou `PAGBANK_LEGACY_BASE_URL`.
