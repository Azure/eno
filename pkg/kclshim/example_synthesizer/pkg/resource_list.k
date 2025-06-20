schema ResultFile:
    path: str
    index?: float

schema ResultResourceRef:
    apiVersion: str
    kind: str
    name: str
    namespace?: str

schema Result:
    message: str
    file?: ResultFile
    resourceRef?: ResultResourceRef
    severity?: str
    tag?: [str]str

schema ResourceList:
    apiVersion: str = "config.kubernetes.io/v1"
    kind: str = "ResourceList"
    items: [any]
    functionConfig?: any
    results?: [Result]