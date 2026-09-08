# Hawser identity

A coiled heavy rope — a hawser is the thick line that moors a ship, and Hawser
is the line that moors stock `docker.exe` to the engine.

| file | use |
|---|---|
| `hawser-mark.svg` | the color mark (slate rope with twist grooves) |
| `hawser-mark-mono.svg` | monochrome silhouette; inherits `currentColor`, for docs / badges / terminals |
| `hawser-logo.svg` | horizontal lockup: mark + `hawser` wordmark |

## Design notes

- One Archimedean-spiral path with a heavy round stroke — pure geometry, no
  fonts and no rasterizer, so it scales exactly from a 16 px tray dot to a
  512 px store tile and the small sizes stay legible (the coil reads as a dense
  spiral-disc when tiny, showing its grooves only as it grows).
- The color mark's twist grooves are a translucent-black overlay, so the rope
  re-colors to any brand hue by changing one value — the grooves follow.
- Deliberately clear of Docker's whale / Moby and the other container-tool
  marks: a rope, nothing else.

## Regenerate

```
go run ./tools/genlogo   # rewrites the three SVGs
```

The rope color is the `brand` constant in `tools/genlogo/main.go`.

## License

Code and docs in this repository are Apache-2.0. The artwork in this directory
is additionally offered under [CC-BY-4.0](https://creativecommons.org/licenses/by/4.0/),
so downstream packagers can carry the mark with attribution.
