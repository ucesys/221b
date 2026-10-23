Windows nodes:
- They run only Windows containers: Linux pods must not be scheduled there (nodeSelector kubernetes.io/os). Networking and storage plugins need Windows support of their own.
