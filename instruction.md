# rule1: I want you to output a .md file named like this <controller_name>_note.md
# rule2: the section should be listed out in the below format
  Section 1: Controller construction and provide a list of resources it manages and watches
  ```
  <This is an example, fill this with the actual controller content>
  func NewController(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1.Symphony{}).
		Owns(&apiv1.Composition{}).
		WithLogConstructor(manager.NewLogConstructor(mgr, "symphonyController")).
		Complete(&symphonyController{
			client:        mgr.GetClient(),
			noCacheClient: mgr.GetAPIReader(),
		})
}
  ```
  Section2: Go over the Reconciliation process and highlight the important functionality focusing on 3 aspects, Creation, Deletion, and update. First at high level and later at peeudo code level like below
  ```
  <This is an example, fill the below block with the actual controller content>
  1. err := c.client.Get(ctx, req.NamespacedName, symph), return return ctrl.Result{}, nil if err is Not found
  2. if controllerutil.AddFinalizer(symph, "eno.azure.io/cleanup") 
  3. err = c.client.List(ctx, existing, client.InNamespace(symph.Namespace), client.MatchingFields by symph name
  4. modified, err := c.reconcileReverse(ctx, symph, existing) -> <explain what this function do>
  5. if symph.DeletionTimestamp != nil --> <explain what happened during deletion>
  6. modified, err = c.reconcileForward(ctx, symph, existing) --> explain what does this function do
  7. err = c.syncStatus(ctx, symph, existing) --> explain what does this function do
  ```
  Section3: Go over the Status aggregration part focusing on how this controller mark when the status becomes ready. Please focus 1) condition that will mark the status become ready, and 2) condition that will mark the status not ready.
  Section4: Please give some ideas on how to improve this controller. Don't need to code up the ideas, just write the ideas down in a list. you can follow the below format
    -------- This is an example and replace it with different controller content suggestions
    Section 4: Improvement Ideas
    1. Performance Optimization: Build Composition Index
    Problem: O(N×M) complexity due to nested linear searches in reconcileForward
    Solution: Build a map index once at the start (map[synthesizerName]*Composition)
    Impact: Reduces complexity from O(N×M) to O(N+M)
    2. Simplify Status Aggregation: Single Loop
    Problem: Two full iterations through all Compositions (find latest, then nullify)
    Solution: Single pass with "all ready" flags, track latest timestamps while checking
    Impact: Reduces from O(2N) to O(N), clearer intent
    3. Merge Annotation Pruning into Main Sync Loop
    Problem: Two separate loops in reconcileForward (prune annotations, then create/update)
    Solution: Combine into single loop with index-based lookup
    Impact: Reduces from 2N lookups to N lookups
# rule3: process this for the asked controller
