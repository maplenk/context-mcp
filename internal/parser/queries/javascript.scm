; JS/TS Tree-sitter queries (for future tree-sitter integration)
; Match standard function declarations
(function_declaration
  name: (identifier) @node.name.function
  body: (statement_block) @node.body) @node.definition.function

; Match arrow functions bound to lexical declarations
(lexical_declaration
  (variable_declarator
    name: (identifier) @node.name.function
    value: (arrow_function) @node.body)) @node.definition.function

; Match function calls
(call_expression
  function: (identifier) @edge.call.target) @edge.call

; Match member expression calls (e.g., console.log)
(call_expression
  function: (member_expression
    object: (identifier) @edge.call.object
    property: (property_identifier) @edge.call.property)) @edge.call
