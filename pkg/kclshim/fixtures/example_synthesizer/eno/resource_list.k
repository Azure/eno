# TODO: Move this file to a KCL module.

schema ResourceList:
    apiVersion: str = "config.kubernetes.io/v1"
    kind: str = "ResourceList"
    items: [any]
    functionConfig?: any
