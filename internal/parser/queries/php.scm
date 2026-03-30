; PHP Tree-sitter queries (for future tree-sitter integration)
; Capture class definitions
(class_declaration
  name: (name) @node.name.class
  body: (declaration_list) @node.body) @node.definition.class

; Capture methods
(method_declaration
  name: (name) @node.name.method
  body: (compound_statement) @node.body) @node.definition.method

; Capture object instantiation
(object_creation_expression
  (name) @edge.instantiates.target) @edge.instantiates
