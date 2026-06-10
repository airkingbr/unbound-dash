# unbound-dash

Dashboard web para monitoramento e gerência de servidores [Unbound](https://nlnetlabs.nl/projects/unbound/about/)
recursivos rodando em Debian.

Binário único em Go (frontend embutido), sem dependências externas. Coleta
estatísticas via `unbound-control stats_noreset` e expõe uma área de
controle para os comandos mais usados do `unbound-control` (reload, flush
de cache, flush de zona, etc).

## Status

Esqueleto inicial:

- [x] API HTTP (login com sessão, `/api/stats`, `/api/status`, `/api/control/{cmd}`)
- [x] Frontend single-page (gráfico de QPS/cache hit, cards de estatísticas, painel de controle)
- [x] Instalador (`scripts/install.sh`) + unit systemd
- [ ] dnstap: estatísticas por IP/subnet, top domínios
- [ ] Histórico persistente (SQLite)
- [ ] Suporte a múltiplos servidores em uma única interface

## Instalação

Em um Debian que já roda Unbound:

```bash
curl -fsSL https://raw.githubusercontent.com/airkingbr/unbound-dash/main/scripts/install.sh -o install.sh
sudo bash install.sh
```

O instalador:

1. Baixa o binário da última release (defina `GITHUB_TOKEN` se o repositório for privado, ou use `-f BINARIO_LOCAL`)
2. Verifica se o `remote-control` do Unbound está habilitado; se não estiver, habilita (com backup do `unbound.conf`)
3. Gera `/etc/unbound-dash/config.json` (com senha de admin, gerada automaticamente se não informada via `-p`)
4. Instala e inicia o serviço systemd `unbound-dash`

Por padrão a interface fica disponível em `http://SEU_SERVIDOR:8080`.

### Opções

```
sudo bash install.sh -p "minha-senha" -l ":8080"
```

## Desenvolvimento

```bash
go build ./...
go run ./cmd/unbound-dash -config ./config.dev.json
```

`config.json` de exemplo:

```json
{
  "listen_addr": ":8080",
  "unbound_control_bin": "/usr/sbin/unbound-control",
  "unbound_conf": "/etc/unbound/unbound.conf",
  "admin_password": "troque-isto"
}
```

## Comandos de controle suportados

`reload`, `status`, `dump_cache`, `flush_requestlist`, `flush_bogus`,
`flush_negative`, `flush <nome>`, `flush_zone <zona>`, `flush_type <nome> <tipo>`,
`flush_infra <ip|all>`, `verbosity <nivel>`.
