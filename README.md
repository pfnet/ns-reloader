# ns-reloader

ns-reloader is a generic Kubernetes namespace watcher that automatically
restarts a wrapped process when namespaces matching a specified label selector
change. It dynamically updates an environment variable with the list of watched
namespaces and gracefully restarts the wrapped process to reflect those changes.

## Configuration

ns-reloader is configured via environment variables:

| Flag                         | Environment Variable                | Required | Description                                                          | Default           |
| ---------------------------- | ----------------------------------- | -------- | -------------------------------------------------------------------- | ----------------- |
| `--namespace-selector`       | `RELOADER_NAMESPACE_SELECTOR`       | Yes      | Kubernetes label selector for namespaces to watch                    | n/a               |
| `--target-env-var`           | `RELOADER_TARGET_ENV_KEY`           | No       | Name of the environment variable to populate with the namespace list | `WATCH_NAMESPACE` |
| `--termination-grace-period` | `RELOADER_TERMINATION_GRACE_PERIOD` | No       | Grace period for stopping the wrapped process                        | `5s`              |
| `--debounce-period`          | `RELOADER_DEBOUNCE_PERIOD`          | No       | Minimum time to wait between process restarts.                       | `5s`              |

## Usage

ns-reloader wraps any process and manages its lifecycle based on namespace
changes. You can use the `--` separator to distinguish reloader options from the
wrapped command.

```text
reloader [OPTION] -- [COMMAND]...
```

### Example: Wrapping KEDA Operator

This example shows how to wrap KEDA operator to dynamically manage autoscaling
across namespaces labeled with `keda.sh/enabled=true`.

```Dockerfile
FROM ghcr.io/pfnet/ns-reloader AS base
FROM ghcr.io/kedacore/keda:2.17.2
COPY --from=base /reloader /reloader
ENTRYPOINT ["/reloader"]
```

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: keda-operator-with-reloader
  namespace: keda
spec:
  replicas: 1
  selector:
    matchLabels:
      app: keda-operator
  template:
    metadata:
      labels:
        app: keda-operator
    spec:
      serviceAccountName: keda-operator
      containers:
        - name: keda-operator
          image: <wapped_keda_operator_image>
          env:
            - name: RELOADER_NAMESPACE_SELECTOR
              value: "keda.sh/enabled=true"
            - name: RELOADER_TARGET_ENV_KEY
              value: "WATCH_NAMESPACE"
          command: ["/reloader"]
          args:
            - "/keda"
            - "--leader-elect"
            - "--zap-log-level=info"
            - "--zap-encoder=console"
```

## License

[Apache License 2.0](LICENSE)
