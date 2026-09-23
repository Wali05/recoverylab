migrate((app) => {
  const collection = new Collection({
    type: "base",
    name: "retry_orders",
    listRule: "",
    viewRule: "",
    createRule: "",
    updateRule: "",
    deleteRule: "",
    fields: [{ name: "clientKey", type: "text", required: true }],
  });
  app.save(collection);
}, (app) => {
  app.delete(app.findCollectionByNameOrId("retry_orders"));
});
