; Go Tree-sitter queries (for future tree-sitter integration)
; Capture struct type definitions
(type_spec
  name: (type_identifier) @node.name.struct
  type: (struct_type) @node.body) @node.definition.struct

; Capture standalone functions
(function_declaration
  name: (identifier) @node.name.function) @node.definition.function

; Capture receiver methods
(method_declaration
  receiver: (parameter_list) @node.receiver
  name: (field_identifier) @node.name.method) @node.definition.method

; Capture cross-package function calls
(call_expression
  function: (selector_expression
    operand: (identifier) @edge.call.package
    field: (field_identifier) @edge.call.target)) @edge.call
