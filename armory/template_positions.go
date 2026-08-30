package armory

import (
	"errors"
	"fmt"
	"text/template"
	"text/template/parse"
)

// payloadPositions contains the positions declared by payload template actions.
type payloadPositions map[int]struct{}

// inspectPayloadPositions validates payload declarations and returns their count.
func inspectPayloadPositions(tmpl *template.Template) (int, error) {
	positions := make(payloadPositions)

	for _, associated := range tmpl.Templates() {
		if associated.Tree == nil || associated.Root == nil {
			continue
		}
		if err := walkTemplateNode(associated.Root, positions); err != nil {
			return 0, err
		}
	}

	if len(positions) == 0 {
		return 0, nil
	}

	maxPosition := -1
	for position := range positions {
		if position > maxPosition {
			maxPosition = position
		}
	}

	for position := 0; position <= maxPosition; position++ {
		if _, exists := positions[position]; !exists {
			return 0, fmt.Errorf("payload position %d is missing", position)
		}
	}

	return maxPosition + 1, nil
}

// walkTemplateNode collects payload declarations from a template syntax tree.
func walkTemplateNode(node parse.Node, positions payloadPositions) error {
	switch node := node.(type) {
	case *parse.ListNode:
		for _, child := range node.Nodes {
			if err := walkTemplateNode(child, positions); err != nil {
				return err
			}
		}
	case *parse.ActionNode:
		return walkTemplateNode(node.Pipe, positions)
	case *parse.PipeNode:
		for _, command := range node.Cmds {
			if err := walkTemplateNode(command, positions); err != nil {
				return err
			}
		}
	case *parse.CommandNode:
		if err := collectPayloadPosition(node, positions); err != nil {
			return err
		}
		for _, argument := range node.Args {
			if err := walkTemplateNode(argument, positions); err != nil {
				return err
			}
		}
	case *parse.IfNode:
		return walkTemplateBranch(node.Pipe, node.List, node.ElseList, positions)
	case *parse.RangeNode:
		return walkTemplateBranch(node.Pipe, node.List, node.ElseList, positions)
	case *parse.WithNode:
		return walkTemplateBranch(node.Pipe, node.List, node.ElseList, positions)
	case *parse.TemplateNode:
		if node.Pipe != nil {
			return walkTemplateNode(node.Pipe, positions)
		}
	case *parse.ChainNode:
		return walkTemplateNode(node.Node, positions)
	}

	return nil
}

// walkTemplateBranch collects payload declarations from each part of a branch.
func walkTemplateBranch(pipe *parse.PipeNode, list, elseList *parse.ListNode, positions payloadPositions) error {
	if pipe != nil {
		if err := walkTemplateNode(pipe, positions); err != nil {
			return err
		}
	}
	if list != nil {
		if err := walkTemplateNode(list, positions); err != nil {
			return err
		}
	}
	if elseList != nil {
		return walkTemplateNode(elseList, positions)
	}

	return nil
}

// collectPayloadPosition validates and records a payload command.
func collectPayloadPosition(command *parse.CommandNode, positions payloadPositions) error {
	if len(command.Args) == 0 {
		return nil
	}

	field, ok := command.Args[0].(*parse.FieldNode)
	if !ok || len(field.Ident) != 1 || field.Ident[0] != "Payload" {
		return nil
	}
	if len(command.Args) != 3 {
		return errors.New(".Payload requires a position and fallback value")
	}

	number, ok := command.Args[1].(*parse.NumberNode)
	if !ok || !number.IsInt {
		return errors.New(".Payload position must be a static integer")
	}

	position := int(number.Int64)
	if int64(position) != number.Int64 {
		return fmt.Errorf(".Payload position %s exceeds integer range", number.Text)
	}
	if position < 0 {
		return errors.New(".Payload position cannot be negative")
	}
	if _, exists := positions[position]; exists {
		return fmt.Errorf("payload position %d is declared more than once", position)
	}

	positions[position] = struct{}{}
	return nil
}
