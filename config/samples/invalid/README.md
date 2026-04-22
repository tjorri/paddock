# Invalid samples

These manifests are intentionally invalid. Applied against a live cluster
with Paddock's validating webhook active, every one of them should be
**rejected** at admission time.

Use them to verify the webhook chain end-to-end:

```sh
for f in config/samples/invalid/*.yaml; do
  echo "--- $f"
  kubectl apply -f "$f" && echo "UNEXPECTED: admitted" || echo "rejected (expected)"
done
```
