# Admission control

A local, scriptable guardrail on the docker API. Hawser already sits in the
request path — every `docker` call crosses the named-pipe bridge — so it can
refuse a container this machine's owner has ruled out, before the engine ever
sees it.

```
hawser policy show                       # the rules in effect
hawser policy check                      # validate the file without applying it
hawser policy test create-body.json      # judge one request, and say why
```

## What it is not

**Not a security boundary against a hostile local user.** They own the
machine: they can edit the rules file, point `DOCKER_HOST` somewhere else, or
talk to the engine directly. This catches mistakes, and gives shared and CI
machines a policy surface. Claiming more would be dishonest.

**Not a rule language.** No Rego, no expressions. The vocabulary is small and
fixed so a reader can tell at a glance what is forbidden — which is most of
the value of writing a policy down.

## The rules

They live in `policy.yaml` in the state dir (`hawser policy show` prints the
path). A missing file means no rules, which is the default state of a machine
nobody has configured.

```yaml
deny-privileged: true                 # refuse --privileged
deny-added-capabilities: true         # refuse any --cap-add
deny-capabilities: [SYS_ADMIN]        # ...or only these
deny-host-namespaces: true            # refuse --network/--pid/--ipc/--uts=host
allow-bind-sources:                   # bind mounts may only come from here
  - C:\work
allow-registries:                     # images may only come from here
  - registry.example.com
  - "*.internal"
require-digest: true                  # images must be pinned by digest
```

Changing the file takes effect on `hawser restart`, the same contract the
audit log has — the supervisor reads it once at start.

### Notes that matter in practice

**`deny-capabilities` is spelling-insensitive.** `SYS_ADMIN`, `sys_admin` and
`CAP_SYS_ADMIN` are the same capability; a rule that only caught one spelling
would be trivially bypassed.

**`allow-bind-sources` matches path prefixes at a boundary.** `C:\work` allows
`C:\work\proj` but not `C:\workshop`. Case and separators do not matter, so
`c:/work` is the same root. **Named volumes are not binds** — `-v myvol:/data`
has no host path to restrict and is never denied by this rule.

**`allow-registries` blocks Docker Hub unless you list it.** Docker's own rule
is that the first component of an image reference is a registry only if it
looks like a host, so `ubuntu` and `library/ubuntu` are both `docker.io`. A
bare hostname in the allowlist matches that host on **any port**; write
`host:5000` if you mean only that port. A leading `*.` matches a whole domain.

**`require-digest`** refuses anything without `@sha256:` — including
`ubuntu:24.04`, because a tag can move.

## What a denial looks like

The bridge answers **403** with the reason, and the docker CLI prints it
verbatim:

```
$ docker run --privileged ubuntu
docker: Error response from daemon: policy denies --privileged: it turns off
container isolation wholesale
```

403 rather than 400 is deliberate: the request is well-formed, this machine
simply will not run it. Every denial is also recorded by the
[audit log](audit.md) when it is enabled.

## Testing a rule set before trusting it

`hawser policy test` judges a container-create body without running anything,
and exits **13** when the rules refuse it — so it can gate a script.

```
$ hawser policy test --rules policy.yaml request.json
DENIED by allow-bind-sources
  policy does not allow bind mounts from C:/secrets (allowed: C:\work)

$ hawser policy test --json --rules policy.yaml request.json
{
  "denied": true,
  "rule": "allow-bind-sources",
  "reason": "policy does not allow bind mounts from C:/secrets (allowed: C:\\work)"
}
```

The body is the JSON the docker CLI POSTs to `/containers/create`. The easiest
way to capture a real one is the [audit log](audit.md); otherwise hand-write
the fields the rule cares about.

## Failure direction

A rules file that will not parse is an **error**, not an empty policy. The
supervisor logs it loudly and runs with admission control **off** rather than
silently enforcing nothing while looking enforced — a guardrail that quietly
fails open is worse than none, because it is believed.

Unknown keys are refused for the same reason: a misspelled rule that silently
does nothing looks exactly like one that works.

```
$ hawser policy check
hawser: policy: yaml: unmarshal errors:
  line 1: field deny-priviliged not found in type policy.Rules
```

Run `hawser policy check` after editing, before restarting.

## Scope today

Only `POST /containers/create` is judged, which is where `--privileged`,
capabilities, namespaces, binds and the image reference all arrive. Resource
caps on an unset container — the one *mutating* rule in the original
proposal — are deliberately not implemented yet: mutating a user's request
silently deserves its own review, and every rule here refuses rather than
edits.
