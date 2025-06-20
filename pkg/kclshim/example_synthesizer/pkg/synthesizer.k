import k8s.api.apps.v1

Synthesize = lambda input: ResourceList -> ResourceList { 
    output = ResourceList {
        items: [
            v1.Deployment {
                
            }
        ]
    }
}