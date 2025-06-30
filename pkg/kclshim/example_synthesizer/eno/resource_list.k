# TODO: Move this file to a KCL module.

schema ResultResourceRef:
    apiVersion: str
    kind: str
    name: str
    namespace?: str

schema Result:
    message: str
    resourceRef?: ResultResourceRef
    severity?: str
    tag?: [str]str

schema ResourceList:
    apiVersion: str = "config.kubernetes.io/v1"
    kind: str = "ResourceList"
    items: [any]
    functionConfig?: any
    results?: [Result]