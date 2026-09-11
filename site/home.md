---
title: "Documentation"
---

Hawser runs the **upstream open source Docker Engine** on Windows, inside WSL2.
No licence fees, no Electron, no Kubernetes: install once and `docker ps` works
forever, on laptops and CI runners alike.

```
hawser install                                  # engine, context, autostart
docker --context hawser run --rm hello-world
```

New here? The [README](https://github.com/hawserhq/hawser#install) has the
install steps and the current status. These pages are the detail behind them —
each one is a markdown file in
[`docs/`](https://github.com/hawserhq/hawser/tree/main/docs), so anything wrong
on this site is a pull request away from being right.
