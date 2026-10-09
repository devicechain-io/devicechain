# Fixture

## valid

```graphql
query {
  things(criteria: {pageNumber: 1, pageSize: 5}) { token name }
}
```

## unknown field

```graphql
query { thingsByToken(tokens: ["a"]) { token noSuchField } }
```

## missing variable decl

```graphql
query { thingsByToken(tokens: $t) { token } }
```

## wrong argument type

```graphql
query { things(criteria: {pageNumber: "one", pageSize: 5}) { token } }
```

## unknown area

```graphql
# schema: nowhere
query { thingsByToken(tokens: ["a"]) { token } }
```

## sdl is skipped

```graphql
type Thing { token: String! }
```

## signature is skipped

```graphql
cancelThing(token: String!): Thing!
```

```json
{ "not": "graphql" }
```
